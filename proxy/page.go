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
<script>try{var __t=localStorage.getItem('theme');if(__t)document.documentElement.setAttribute('data-theme',__t);}catch(e){}</script>
<style>
  :root { --bg:#09090b; --panel:#18181b; --border:#27272a; --fg:#fafafa; --muted:#a1a1aa;
          --accent:#fafafa; --accent-hover:#e4e4e7; --accent-fg:#18181b; --danger:#ef4444; --ring:rgba(250,250,250,.30); }
  :root[data-theme="light"] { --bg:#ffffff; --panel:#ffffff; --border:#e4e4e7; --fg:#09090b; --muted:#71717a;
          --accent:#18181b; --accent-hover:#27272a; --accent-fg:#fafafa; --danger:#dc2626; --ring:rgba(24,24,27,.22); }
  body { font-family: system-ui, -apple-system, sans-serif; background: var(--bg); color: var(--fg);
         display: flex; min-height: 100vh; align-items: center; justify-content: center; margin: 0; }
  form { background: var(--panel); padding: 1.75rem; border-radius: 12px; border: 1px solid var(--border);
         width: 280px; box-shadow: 0 12px 32px rgba(0,0,0,.55); }
  h1 { font-size: 1.05rem; font-weight: 600; margin: 0 0 1.1rem; text-align: center; }
  input { width: 100%; box-sizing: border-box; padding: .7rem; font-size: 1.4rem;
          letter-spacing: .4em; text-align: center; border-radius: 8px;
          border: 1px solid var(--border); background: var(--bg); color: var(--fg);
          transition: border-color .12s ease, box-shadow .12s ease; }
  input:focus { outline: none; border-color: var(--accent); box-shadow: 0 0 0 3px var(--ring); }
  button { width: 100%; margin-top: 1rem; padding: .7rem; font-size: 1rem; cursor: pointer;
           border: none; border-radius: 8px; background: var(--accent); color: var(--accent-fg); font-weight: 600; }
  button:hover { background: var(--accent-hover); }
  .err { color: var(--danger); font-size: .85rem; margin: 0 0 1rem; }
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
