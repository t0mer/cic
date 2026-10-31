// Applies the saved/system theme before first paint (external file: strict CSP, no inline scripts).
;(function () {
  var dark = window.matchMedia('(prefers-color-scheme: dark)').matches
  try {
    var t = localStorage.getItem('cic-theme')
    if (t === 'dark') dark = true
    else if (t === 'light') dark = false
  } catch (_) {
    /* storage unavailable: fall back to system */
  }
  document.documentElement.classList.toggle('dark', dark)
})()
