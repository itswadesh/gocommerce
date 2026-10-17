# Signing up, signing in and getting back in, on a phone

Owner's request, 2026-10-17: "Improve mobile login page for gocommerce, +
signup + forgot password. There is no way to signup and create a new store.
Password reset must send a link inside email clicking which must be able to
create new password."

## What is there today

- Sign-in is a PocketBase card drawn by the root layout at `/admin/auth/login`.
  On a 360px phone the fields run to the edge of the glass, the card sits in
  the vertical middle (so it jumps when the keyboard opens), and it is titled
  "Superuser login".
- "Forgot your password?" is a second mode of that card, not an address, so a
  phone's back button leaves the panel instead of returning to sign-in.
- The engine's reset flow is complete: a hash-only token, an hour, one use,
  every session ended, signed in afterwards. The email carries a link when
  `Config.PanelURL` (`GOCOMMERCE_PANEL_URL`) is set, and a code to paste when
  it is not, because a link built from the request's host would let anybody
  mail a real operator a link to a host they chose.
- Nobody can make an account for themselves. A person exists only through the
  installer (the first one), a team screen, or an invitation. A signed-in
  person can open a store from the store picker (`POST /api/stores/create`).

## What changes

### Engine

1. `Config.Signup` (`-signup`, `GOCOMMERCE_SIGNUP`), off by default. An install
   that hosts stores for other people turns it on. A self-hosted shop leaves it
   off and never takes strangers.
2. `POST /api/admin/signup` `{email, password}`: makes a person with no store,
   and signs them in (the sign-in response's shape, 201). Off: 403
   `signup_closed`. An address with an account already: 409 `email_taken`,
   saying to sign in or reset. Throttled per client address with a throttle of
   its own, charged on every attempt.
3. `GET /api/admin/auth-state` says `signup`.
4. A reset request's 202 says `link`: whether the email carries a link
   (PanelURL is set) or a code. It is configuration, the same for every
   caller, so it says nothing about the address.

The person has no store yet, and the store is opened on the next screen by the
existing create form, so `POST /api/stores/create` stays the one way a store is
opened (its suggested name, country and currency included).

### Panel

- One signed-out layout, `AuthShell`, for sign-in, sign-up, forgot password,
  reset password, accepting an invitation and the store picker: a brand bar,
  then the form top-aligned with a 20px gutter on a phone, and a bordered card
  centred on a wider screen. Full-size fields and buttons (45–52px), and 16px
  text in inputs on a phone so iOS does not zoom.
- `/admin/auth/login`: "Sign in", "Forgot password?" beside the password when a
  link can be sent (the honest sentence otherwise), and "New here? Create a
  store" when sign-up is open.
- `/admin/auth/signup`: email and password, then the store picker's create form
  ("Create your first store") as step two.
- `/admin/auth/forgot-password`: its own address, the email carried over from
  sign-in, and "Check your inbox" saying link or code as the engine reports it.
- `/admin/auth/reset-password/[[token]]`: unchanged in behaviour, in the new
  layout.
- Motion: the card rises in; moving between these screens is a view
  transition of the card alone; buttons sink under a press; errors rise in.
  With reduced motion, each of these fades instead.

### For the hosted service

`GOCOMMERCE_SIGNUP=1` opens sign-up, and `GOCOMMERCE_PANEL_URL=https://admin.kitcommerce.store`
makes every reset email carry a link. Both are environment settings for the
owner to set on the server.

## Not doing

- Email verification before an account works. It would make opening a store
  depend on the install's mail. An account made with somebody else's address
  becomes theirs the moment they reset its password.
- A reset link built from the request's host when PanelURL is unset (the
  reason is above and in `passwordreset.go`).
