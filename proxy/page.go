package main

import "html"

// loginPage renders the minimal login form. msg, if non-empty, is shown as an
// error and is HTML-escaped before output.
func loginPage(msg string) string {
	var banner string
	if msg != "" {
		banner = `<p class="err">` + html.EscapeString(msg) + `</p>`
	}
	return `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Authentication required</title>
<style>
  body { font-family: system-ui, sans-serif; background: #0d1117; color: #c9d1d9;
         display: flex; min-height: 100vh; align-items: center; justify-content: center; margin: 0; }
  form { background: #161b22; padding: 2rem; border-radius: 10px; border: 1px solid #30363d;
         width: 280px; box-shadow: 0 8px 24px rgba(0,0,0,.4); }
  h1 { font-size: 1.1rem; margin: 0 0 1rem; }
  input { width: 100%; box-sizing: border-box; padding: .7rem; font-size: 1.4rem;
          letter-spacing: .4em; text-align: center; border-radius: 6px;
          border: 1px solid #30363d; background: #0d1117; color: #c9d1d9; }
  button { width: 100%; margin-top: 1rem; padding: .7rem; font-size: 1rem; cursor: pointer;
           border: none; border-radius: 6px; background: #238636; color: #fff; }
  button:hover { background: #2ea043; }
  .err { color: #f85149; font-size: .85rem; margin: 0 0 1rem; }
</style>
</head>
<body>
  <form method="post" action="/auth/login">
    <h1>Enter your 6-digit code</h1>` + banner + `
    <input name="code" inputmode="numeric" pattern="[0-9]*" maxlength="6"
           autocomplete="one-time-code" autofocus placeholder="000000">
    <button type="submit">Unlock</button>
  </form>
</body>
</html>`
}
