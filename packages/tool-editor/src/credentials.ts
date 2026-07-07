// Thin wrapper around the Credential Management API's PasswordCredential,
// used to explicitly prompt the browser's password manager to offer saving
// after a successful login/register.
//
// Why this exists: this app is a SPA that submits via fetch (see api.ts),
// not a real <form> POST/page navigation. Browsers' heuristic "was this a
// successful login?" detection for the native save-password prompt is
// tuned for traditional form submissions and is unreliable for fetch-based
// SPAs — the prompt often just doesn't appear, which is what led here.
// navigator.credentials.store() sidesteps the heuristic entirely: it's an
// explicit "save this, I'm telling you it succeeded" call.
//
// PasswordCredential isn't in TypeScript's standard DOM lib (it's part of
// the Credential Management spec, which Firefox/Safari don't implement),
// so this does its own feature detection rather than assuming the type
// exists, and is a silent no-op anywhere it's unsupported.
export async function offerToSavePassword(email: string, password: string): Promise<void> {
  const w = window as unknown as {
    PasswordCredential?: new (data: { id: string; password: string }) => Credential
  }
  if (!w.PasswordCredential || !navigator.credentials?.store) return

  try {
    const cred = new w.PasswordCredential({ id: email, password })
    await navigator.credentials.store(cred)
  } catch {
    // Not fatal — the user just won't get the save-password prompt this
    // time (e.g. the browser declined, or a permissions-policy blocked
    // it). Login itself already succeeded before this is ever called.
  }
}
