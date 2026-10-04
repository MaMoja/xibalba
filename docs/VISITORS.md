# Why am I seeing a security check?

You came here because a website showed you a page titled "A quick security
check", "This request was blocked" or "Too many requests", with the line
"Protected by Xibalba". This page explains what happened.

## The security check

The website you wanted to visit is protected against automated mass
requests. Programs that copy entire websites, many of them collecting text
to train AI systems, send so many requests that they slow sites down or take
them offline. The operator of the website uses Xibalba to keep them out.

To tell a browser with a person in front of it from such a program, the page
asks your browser to solve a short calculation. This usually takes a few
seconds and you do not have to do anything. For one visitor that is nothing;
for a program that asks for a million pages it adds up.

Afterwards you are taken to the page you asked for, and you are not checked
again for a while (a week, unless the operator set something else).

## What it does not do

- It does not identify you. The calculation needs no account, no name and no
  personal data.
- It does not track you. One cookie is stored that only records that the
  check was passed. It holds no identifier of you and nothing about the
  pages you visit, and it is not passed on to the website.
- It loads nothing from other companies: no fonts, no scripts, no
  advertising, no analytics.
- It is not mining anything. The calculation has no value beyond showing
  that your browser did it.

## If the check does not finish

| What you see | What to do |
|---|---|
| The page stays at "The check is running …" | Give it a little longer on an old or busy device. If nothing happens, reload the page. |
| "Your browser does not run JavaScript" with a button | JavaScript is off or blocked by an extension. Wait a few seconds and choose "Continue". This path works without JavaScript. |
| "JavaScript must be switched on for this check" | The operator of this website requires JavaScript for the check. Switch it on for this site, or allow the site in your script-blocking extension, and reload. |
| The check appears again at every page | Your browser does not keep the cookie. Allow cookies for this website. In a private window the cookie is gone when you close the window. |
| "That was a little too fast" | You chose "Continue" before the short wait was over. Wait a few seconds and choose it again. |

Extensions that block scripts or change what your browser reports about
itself can interfere with the check. If it fails, try once with extensions
switched off for this site.

## "This request was blocked"

The operator of the website does not allow requests of this kind. If you
think this is a mistake, get in touch with the operator of the website and
quote the reference shown on the page. The reference names the rule that
decided, not you. If the page shows a contact, use that.

## "Too many requests"

A large number of requests came from your connection in a short time. This
can happen when many people share one connection (an office, a school, a
mobile network). Wait a little and try again.

## Who to ask

The check is run by the operator of the website you visited, on their own
server. The Xibalba project provides the software and has no access to that
server, to its settings or to any information about you. For questions about
a particular website, ask its operator.
