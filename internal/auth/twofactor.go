package auth

import (
	"errors"
	"net/http"
	"strings"
)

const totpIssuer = "CIC"

// login2FA completes a two-step login: POST /login/2fa {mfa_token, code}.
func (s *Service) login2FA(w http.ResponseWriter, r *http.Request) {
	if !s.sameOrigin(r) {
		writeError(w, http.StatusForbidden, "cross-site request rejected")
		return
	}
	var req struct {
		Token string `json:"mfa_token"`
		Code  string `json:"code"`
	}
	if !decode(w, r, &req) {
		return
	}
	const expired = "login expired, sign in again"
	ipKey := ipPrefix + limiterIP(ClientIP(r, s.trusted))
	uid, ok := s.mfa.peek(req.Token)
	if !ok {
		// Unknown token: charge the source address only, so token spraying is slowed.
		att, wait := s.limiter.Begin(ipKey)
		if att == nil {
			tooMany(w, wait)
			return
		}
		att.Fail()
		writeError(w, http.StatusUnauthorized, expired)
		return
	}
	u, err := s.store.UserByID(r.Context(), uid)
	if err != nil && !errors.Is(err, ErrUserMissing) {
		if r.Context().Err() != nil {
			return
		}
		s.log.Error("2fa lookup", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if err != nil || u.Disabled || !u.TOTPEnabled {
		s.mfa.drop(req.Token)
		writeError(w, http.StatusUnauthorized, expired)
		return
	}
	userKey := "user:" + strings.ToLower(u.Username)
	att, wait := s.limiter.Begin(ipKey, userKey)
	if att == nil {
		tooMany(w, wait)
		return
	}
	defer att.Cancel()
	if !s.mfa.try(req.Token) {
		att.Fail()
		writeError(w, http.StatusUnauthorized, expired)
		return
	}
	f, err := s.store.VerifySecondFactor(r.Context(), u.ID, req.Code, s.now())
	switch {
	case err == nil:
	case errors.Is(err, ErrBadCode):
		att.Fail()
		s.mfa.dropIfSpent(req.Token)
		writeError(w, http.StatusUnauthorized, "invalid code")
		return
	case errors.Is(err, ErrTOTPNotEnabled):
		s.mfa.drop(req.Token)
		writeError(w, http.StatusUnauthorized, expired)
		return
	default:
		s.mfa.refund(req.Token)
		s.twoFAServerError(w, r, "verifying second factor", err)
		return
	}
	if !s.mfa.take(req.Token) { // lost a race with a parallel request: single use
		writeError(w, http.StatusUnauthorized, expired)
		return
	}
	att.Success(userKey)
	if f == FactorBackup {
		s.auditEvent(r.Context(), u.Username, "2fa_login", "factor=backup_code")
	}
	s.startSession(w, r, u, false)
}

// twoFAServerError answers a non-verdict failure: 503 without a key, 500 otherwise. A
// cancelled request gets no answer. The limiter is never touched by it.
func (s *Service) twoFAServerError(w http.ResponseWriter, r *http.Request, what string, err error) {
	if r.Context().Err() != nil {
		return
	}
	if errors.Is(err, ErrNoSecretKey) {
		writeError(w, http.StatusServiceUnavailable, ErrNoSecretKey.Error())
		return
	}
	s.log.Error(what, "err", err)
	writeError(w, http.StatusInternalServerError, "internal error")
}

func noStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
}

func (s *Service) twoFAStatus(w http.ResponseWriter, r *http.Request) {
	u, _ := UserFrom(r.Context())
	en, rem, err := s.store.TOTPState(r.Context(), u.ID)
	if err != nil {
		s.twoFAServerError(w, r, "reading 2fa state", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": en, "backup_codes_remaining": rem})
}

// twoFAGate admits a sensitive 2FA request: it takes the user's "2fa:" limiter slot and,
// when withPassword, checks current_password. The returned attempt is settled by the caller
// (Fail on a wrong code, Success when done); a nil attempt means the response was written.
func (s *Service) twoFAGate(w http.ResponseWriter, r *http.Request, u User, password string, withPassword bool) (*Attempt, string) {
	key := "2fa:" + strings.ToLower(u.Username)
	att, wait := s.limiter.Begin(key)
	if att == nil {
		tooMany(w, wait)
		return nil, key
	}
	if !withPassword {
		return att, key
	}
	hash, err := s.store.passwordHash(r.Context(), u.ID)
	if err != nil {
		att.Cancel()
		if r.Context().Err() == nil {
			s.log.Error("2fa password lookup", "err", err)
			writeError(w, http.StatusInternalServerError, "internal error")
		}
		return nil, key
	}
	ok, _, err := s.verify(r.Context(), password, hash, true)
	if err != nil {
		att.Cancel()
		verdictErr(w, err)
		return nil, key
	}
	if !ok {
		att.Fail()
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return nil, key
	}
	return att, key
}

func (s *Service) twoFASetup(w http.ResponseWriter, r *http.Request) {
	u, _ := UserFrom(r.Context())
	var req struct {
		Password string `json:"current_password"`
	}
	if !decode(w, r, &req) {
		return
	}
	// Setup is only a password proof, never a second-factor proof, so it must not clear the
	// shared "2fa:" failures (that would launder wrong codes sent to disable/backup-codes).
	// The attempt is always Cancelled (deferred), which refunds it without a reset.
	att, _ := s.twoFAGate(w, r, u, req.Password, true)
	if att == nil {
		return
	}
	defer att.Cancel()
	secret, err := GenerateSecret()
	if err != nil {
		s.twoFAServerError(w, r, "generating totp secret", err)
		return
	}
	if err := s.store.SetPendingTOTP(r.Context(), u.ID, secret); err != nil {
		if errors.Is(err, ErrTOTPEnabled) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		s.twoFAServerError(w, r, "storing pending totp secret", err)
		return
	}
	noStore(w)
	writeJSON(w, http.StatusOK, map[string]string{"secret": secret, "otpauth_uri": OTPAuthURI(totpIssuer, u.Username, secret)})
}

func (s *Service) twoFAEnable(w http.ResponseWriter, r *http.Request) {
	u, _ := UserFrom(r.Context())
	sess, _ := SessionFrom(r.Context())
	var req struct {
		Code string `json:"code"`
	}
	if !decode(w, r, &req) {
		return
	}
	att, key := s.twoFAGate(w, r, u, "", false)
	if att == nil {
		return
	}
	defer att.Cancel()
	codes, err := s.store.EnableTOTP(r.Context(), u.ID, req.Code, s.now())
	switch {
	case err == nil:
	case errors.Is(err, ErrBadCode):
		att.Fail()
		writeError(w, http.StatusUnauthorized, "invalid code")
		return
	case errors.Is(err, ErrTOTPEnabled), errors.Is(err, ErrTOTPNotPending):
		writeError(w, http.StatusConflict, err.Error())
		return
	default:
		s.twoFAServerError(w, r, "enabling totp", err)
		return
	}
	att.Success(key)
	if err := s.store.DeleteOtherSessions(r.Context(), u.ID, sess.IDHash); err != nil {
		s.log.Error("revoking other sessions", "err", err)
	}
	s.auditEvent(r.Context(), u.Username, "2fa_enabled", "")
	noStore(w)
	writeJSON(w, http.StatusOK, map[string]any{"backup_codes": codes})
}

func (s *Service) twoFADisable(w http.ResponseWriter, r *http.Request) {
	u, _ := UserFrom(r.Context())
	sess, _ := SessionFrom(r.Context())
	var req struct {
		Password string `json:"current_password"`
		Code     string `json:"code"`
	}
	if !decode(w, r, &req) {
		return
	}
	att, key := s.twoFAGate(w, r, u, req.Password, true)
	if att == nil {
		return
	}
	defer att.Cancel()
	if !s.checkSecondFactor(w, r, att, u, req.Code) {
		return
	}
	if err := s.store.DisableTOTP(r.Context(), u.ID); err != nil {
		s.twoFAServerError(w, r, "disabling totp", err)
		return
	}
	att.Success(key)
	if err := s.store.DeleteOtherSessions(r.Context(), u.ID, sess.IDHash); err != nil {
		s.log.Error("revoking other sessions", "err", err)
	}
	s.auditEvent(r.Context(), u.Username, "2fa_disabled", "")
	noStore(w)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) twoFABackupCodes(w http.ResponseWriter, r *http.Request) {
	u, _ := UserFrom(r.Context())
	var req struct {
		Password string `json:"current_password"`
		Code     string `json:"code"`
	}
	if !decode(w, r, &req) {
		return
	}
	att, key := s.twoFAGate(w, r, u, req.Password, true)
	if att == nil {
		return
	}
	defer att.Cancel()
	if !s.checkSecondFactor(w, r, att, u, req.Code) {
		return
	}
	codes, err := s.store.RegenerateBackupCodes(r.Context(), u.ID)
	if err != nil {
		if errors.Is(err, ErrTOTPNotEnabled) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		s.twoFAServerError(w, r, "regenerating backup codes", err)
		return
	}
	att.Success(key)
	s.auditEvent(r.Context(), u.Username, "2fa_backup_regenerated", "")
	noStore(w)
	writeJSON(w, http.StatusOK, map[string]any{"backup_codes": codes})
}

// checkSecondFactor verifies code for an authenticated user (TOTP step or backup code) and
// writes the error response itself, returning false, when it does not pass.
func (s *Service) checkSecondFactor(w http.ResponseWriter, r *http.Request, att *Attempt, u User, code string) bool {
	_, err := s.store.VerifySecondFactor(r.Context(), u.ID, code, s.now())
	switch {
	case err == nil:
		return true
	case errors.Is(err, ErrBadCode):
		att.Fail()
		writeError(w, http.StatusUnauthorized, "invalid code")
	case errors.Is(err, ErrTOTPNotEnabled):
		writeError(w, http.StatusConflict, err.Error())
	default:
		s.twoFAServerError(w, r, "verifying second factor", err)
	}
	return false
}
