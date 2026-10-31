// Package commands sends remote commands (ping, notify, SMS, status and
// location requests) to DRADIS devices over MQTT.
//
// Every command is validated, rate limited per authenticated user and
// published QoS 1, never retained, to a topic built from a registered device.
// Rate limits per user per minute: ping and stop-ping share 10, notify 10,
// sms 5, getstatus 20, getlocation 20. A slot is taken only after validation
// passed and the device is known.
//
// SMS sends are additionally capped per UTC day in billed segments (see
// SMSSegments), limited to 3 per recipient number per rolling hour, and their
// phone-side results are correlated by Correlator. The segments are reserved
// before publishing and given back only when the failure proves nothing was
// sent (broker not connected, rejected topic, context already cancelled). On a
// timeout, cancellation or connection loss while waiting for the broker the
// message may still be delivered, so the reservation is kept and
// ErrOutcomeUnknown is returned.
//
// The wiring MUST call Correlator.Sweep(time.Now()) about every 5 seconds;
// nothing else reports SMS whose result never arrived or frees their entries.
//
// Phone numbers and message bodies are never logged; MaskPhone and Preview
// produce the only forms safe for an audit log (Preview: audit log only).
//
// # Error to HTTP status
//
//	ErrInvalid           400  bad input (also a rejected topic)
//	ErrSMSDisabled       403  commands.sms_daily_cap is 0
//	ErrUnknownDevice     404
//	ErrRateLimited       429  per-user per-minute bucket
//	ErrRecipientLimited  429  3 SMS per number per hour
//	ErrSMSCap            429  daily segment cap
//	ErrNotConnected      503  broker down, nothing sent
//	ErrUnavailable       503  store failure, nothing sent (500 is also reasonable)
//	ErrOutcomeUnknown    504  may have been delivered; do not blindly retry
//	context.Canceled     the caller went away; no response needed
package commands
