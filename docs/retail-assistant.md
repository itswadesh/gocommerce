# Retail assistant

`ext/retailassistant` keeps a shop's supplier accounts from what it forwards on
WhatsApp. A bill arrives on one number shared by every store on the install, is
read as far as a deterministic reader can, waits for a person to review it, and
becomes a payable. Payments and credit notes settle it. The decision and its
costs are D192 in [`PLAN.md`](../PLAN.md).

This is a first slice. It has been tested against a stand-in for Meta and with
made-up documents, and not against Meta's real API or a real supplier's bills.
**Do not call it production-ready until both have been done.** See
[What has not been tested](#what-has-not-been-tested).

## What it does and does not do

| It does | It does not |
|---|---|
| Takes files and photos on one shared WhatsApp number | Read PDFs or photos unless a store turns on AI reading: otherwise a person enters them |
| Reads tables in CSV, XLSX and DOCX files | Post anything without a person approving it |
| Keeps each store's records apart, behind the store policy | Keep a file from a number no store has linked |
| Tracks partial payments, advances, credit notes and reversals | Treat a payment screenshot as proof money moved |
| Books goods into stock when somebody says they arrived | Move stock because a bill arrived |

Not built at the counter: working offline, printing a receipt, batch and expiry, and
machines such as the NGX SP92TB, whose maker documents no API or export.

## Setting it up

### 1. Meta

You need a [WhatsApp Business Platform](https://developers.facebook.com/docs/whatsapp/cloud-api/)
app and a phone number on it. From the app, note its **App secret**, the number's
**Phone number ID**, and a **permanent access token** with permission to send
messages and read media. Choose a **verify token** yourself, any long random string.

### 2. The install

Start the engine with `-retail-assistant`, and set these in its environment. They
are the install's, not a store's. In `docker-compose.yml` they are already passed
through.

| Variable | What it is |
|---|---|
| `RETAIL_ASSISTANT_APP_SECRET` | Checks that a webhook really came from Meta |
| `RETAIL_ASSISTANT_VERIFY_TOKEN` | The string you chose above |
| `RETAIL_ASSISTANT_ACCESS_TOKEN` | Fetches files and sends replies |
| `RETAIL_ASSISTANT_PHONE_NUMBER_ID` | The shared number's id at Meta |
| `RETAIL_ASSISTANT_DISPLAY_NUMBER` | The number as people dial it, shown when a store links a phone |
| `RETAIL_ASSISTANT_QUIET_TO_STRANGERS` | `1` when the number is shared with another service: nobody unlinked gets a reply |
| `RETAIL_ASSISTANT_TEMPLATE` | Optional. The name of a Meta-approved template for messages outside the 24-hour window (below) |
| `RETAIL_ASSISTANT_LOGIN_TEMPLATE` | Optional. The name of a Meta-approved authentication template that carries panel sign-in codes (below) |
| `RETAIL_ASSISTANT_FILE_KEY` | Optional. 32 random bytes in base64 that encrypt new files in the database |
| `RETAIL_ASSISTANT_CLAMD_ADDR` | Optional. The `host:port` of a ClamAV daemon that scans new files |

With any of the first four unset, webhooks are refused or nothing can be sent,
and the panel says the number is not set up.

### 3. The webhook

In the Meta app, set the callback URL and subscribe to the `messages` field:

```
https://<your panel's host>/x/retailassistant/whatsapp/webhook?store=<any store's slug>
```

The `?store=` is required on an install with several stores, because the engine
binds every `/x/` request to a store before a module sees it. It does not decide
where messages go: the sender's number does. Any active store's slug will do, so
choose one that will not be deleted.

### Sharing a number with another service

A WhatsApp number sends its webhooks to one address. If another service already
holds it, leave the callback there and have that service pass on each callback
unchanged: the exact body and the `X-Hub-Signature-256` header, to the URL above.
Use the same app's secret, so the signature still checks out. Set
`RETAIL_ASSISTANT_QUIET_TO_STRANGERS=1` too, or that service's customers will be
told their number is not linked to a store.

### 4. Each store

Under **Orders › Supplier bills**, the owner switches the assistant on.

A phone someone already signs in with needs nothing more. Any message from it to the
shared number links it, and a bill sent first is kept, as long as that person proved
the phone with a code, works in the store and has the right to link numbers. A
seller's account does not count, and a phone the store removed stays removed.

For any other phone, the owner adds it with its country code. The screen shows a
code, once. From that phone, send `LINK 123456` to the shared number. Until then,
nothing from that phone is kept.

One phone can be linked to several stores. What it sends goes to its primary store,
the oldest of them, and the reply says so once and names the others. Replying
`STORE <store's slug>` moves it to another store until it says otherwise. Every bill
still waits for review, and a bill in the wrong store can be rejected there.

Confirmation messages are off until the owner ticks **Send** beside a phone. Within
23 hours of the phone last writing they go as ordinary text, an hour inside Meta's
24-hour window. After that they need a template:

1. In WhatsApp Manager, create a **Utility** template with one variable in its body,
   such as `Update from your shop assistant: {{1}} Reply STOP to end these messages.`, in English (`en`) and Hindi
   (`hi`), and wait for Meta to approve both. Choosing the category and getting it
   approved is yours: Meta may change or reject it.
2. Set `RETAIL_ASSISTANT_TEMPLATE` to its name.

With no template, a message outside the window is marked undelivered with the reason
and is not retried.

**Phones and language.** A phone can reply `HINDI` or `ENGLISH` to change the language
of everything sent to it, or the owner can set it beside the phone. Before a phone is
linked, replies come in both. **The Hindi is a machine translation that no native
speaker has reviewed; have one read `ext/retailassistant/messages.go` before showing it
to shopkeepers.**

The retail assistant's screens have an **English / हिंदी** switch, remembered in each
browser. **That Hindi is a machine translation too, in `admin/src/lib/retail-hindi.js`.**
The sidebar, the rest of the panel, and messages the engine writes, such as a refused
amount or what is wrong with a bill, stay in English. The screens' Hindi has not been
read by a native speaker.

**Stopping.** A phone can reply `STOP`, or `बंद`, to end confirmations in every store
it is linked to, including any already queued. It can still send bills. An owner cannot
tick it back on: the phone has to send `START`.

## Signing in with a WhatsApp code

The panel's sign-in screen already takes a phone number and asks for a code (D193).
This sends that code on WhatsApp. Nothing else about signing in changes: the engine
makes the code, limits the tries, checks it and issues the session, and a phone being
linked to a store gives nobody a way in. A person still needs an operator account with
that phone on it.

1. In WhatsApp Manager, create an **Authentication** template with a copy-code button, in
   English (`en`) and Hindi (`hi`), and wait for Meta to approve it.
2. Set `RETAIL_ASSISTANT_LOGIN_TEMPLATE` to its name.

It is the install's choice, for every number, because the answer to asking for a code
must not depend on whose number it is.

- **With MSG91** set up for sign-in codes, a person gets the same code by both SMS and
  WhatsApp, and you pay for both. Remove the sign-in template from MSG91 to stop the SMS.
- **With Twilio** or another SMS provider, WhatsApp takes over sign-in codes, so someone
  not on WhatsApp cannot get one and has to sign in by email and password.

The screen may still say the code was texted. Meta's real authentication template has
not been tried.

## Reading PDFs and photos with AI

CSV, XLSX and DOCX tables are read exactly by the engine. A PDF or a photo needs a
model, so it stays off until both of these are done:

1. The install runs with `-ai-gateway`, and the store's **AI gateway** plugin can
   make calls and has **Share documents** on.
2. **Read PDFs and photos with AI** is on in the settings under **Orders › Supplier
   bills**. This is separate on purpose: using AI for product listings is not
   agreeing to send supplier bills to a provider.

A new PDF or photo is then sent through the gateway, under its spending cap, and
the review form fills in from what comes back. **Read with AI** asks again after a
failure. Amounts are read as text and through the same parser as a typed amount.
A smaller model's reading goes to a stronger one when it is unsure, or when its
figures do not add up. An amount that cannot be parsed fails the reading, and a
person types the document in, as they would with no AI.

It can still misread a figure and look right. So a document a model read stops at
review like any other, and the engine refuses to approve it until a person says
they compared it with the file; saying so goes in the audit trail. A person's typing
is never overwritten by a reading that arrives late.

The accuracy on real bills has not been measured. Try it on real documents before
relying on it.

## Keeping files safe

**Encryption.** Make a key with `openssl rand -base64 32` and set
`RETAIL_ASSISTANT_FILE_KEY`. New files are then sealed with AES-256-GCM; files
already kept stay readable as they were. **Keep the key somewhere other than the
database's backups, and do not lose it: a file sealed with a lost key cannot be
opened.** The ledger is unaffected, since only the original files are sealed, and
what was read from a file is not. A mistyped key does not stop the engine: each new
file fails with the reason, and none is stored in clear until the key is fixed.
There is no key rotation yet.

**Virus scanning.** Run [ClamAV's daemon](https://docs.clamav.net/) and set
`RETAIL_ASSISTANT_CLAMD_ADDR` to its address. A flagged file is not kept, and its
document shows as failed with the reason. While the scanner is down, files wait and
are retried, and after five tries the document fails. **Try again** fetches it from
Meta again, which works only while Meta still holds the file.

**Deleting files.** Under **Orders › Supplier bills**, **Settings** takes a number
of days. Past that, counted from the review, a posted document loses its file, and
a rejected or failed one also loses what was read from it. Supplier accounts never
change. Empty keeps files for good. Check with your accountant how long you have to
keep bills before setting this. A reviewed document's own **Delete file** button
does the same for one file, at any time.

**Exports.** **Orders › Suppliers** downloads balances, bills, payments and credit
notes as CSV for an accountant.

## Matching a bank statement

A screenshot does not prove money moved, and a bank statement does. Under **Orders ›
Bank statement**, upload your statement as a CSV. If your bank only gives Excel, open
it and save it as CSV. Money going out is listed, and each line suggests recorded
payments of exactly that amount within a week of its date.

- **This is it** ties the line to that payment, and verifies it if it was not.
- **Record as paid to them** makes a verified payment from a line nobody recorded.
- **Not a supplier payment** sets a line aside.
- **Undo** puts a line back. A payment that match verified goes back to not verified,
  unless money from it is allocated to a bill, in which case reverse that first.

Uploading an overlapping statement adds only what is new. A row whose date or amount
cannot be read is left out and counted, so check those against the file. The column
names are a first guess at how Indian banks lay out a CSV, and have not met a real
statement. One transfer that pays two recorded payments cannot be matched yet.

## Shop insights

**Orders › Shop insights** shows what you owe, what you bought and paid in 30 days, what
needs doing, who is owed most, what is running low, and what has not sold in 90 days.
Every figure comes from your records, and no AI is involved. Someone who may not read
orders and inventory sees only the supplier figures.

It says when it cannot know something. Until sales records reach back 90 days it will
not call anything slow, because no sales recorded is not the same as no sales.

**Send a morning summary** in the settings sends each phone taking confirmations what
you owe and what is waiting, once a day around 8 in the morning, India time. Outside
the 24-hour window it needs the template, and each one may cost a WhatsApp message.

Asking questions in plain language on WhatsApp is not built.

## The shop counter

**Orders › Shop counter** is a fast checkout for a phone or tablet. A Bluetooth or USB
barcode scanner types like a keyboard, so the box stays ready: scan, and the item is on
the bill. The same item again is one more.

- A sale is an ordinary order, made through the engine's own order path, so it takes
  stock once, under the same locks as any order. It shows in Orders and Reports. Return
  it there, and tick restock to put it back on the shelf.
- The total comes from the engine. Nothing is charged for delivery, and no email is sent.
- **Choosing UPI is not proof of money.** The sale is paid only if the cashier ticks that
  they saw it arrive, and otherwise it is recorded as not paid yet. Credit is the same.
  Mark it paid on the order when the money comes.
- A cashier's discount is capped by **Most discount at the counter** in the settings, and
  is 0 until the owner sets it.
- An unknown barcode asks which product it is, and is saved on that product if the person
  may change the catalogue.
- A scanner has not been tried on a real phone. Check one at the shop before relying on it.

Set **Counter's state** and **Counter's country code** in the settings, since tax on a
counter sale is worked out from where the counter is.

Staff can use the counter through the `retailassistant.counter` right, and see nothing of
what the shop owes its suppliers.

## Wholesale quotes

A wholesaler can turn a manufacturer's bill or price list into a quote for one retailer
without rebuilding a spreadsheet. It needs `-b2b`, the retailer as a company with a
buyer, and the `quotes.write` and `retailassistant.write` rights.

Under **Orders › Wholesale quote**, choose a document, a retailer, and a rule. The two
rules are different prices, and the screen says so: 20% **markup** on ₹100 is ₹120, and
20% **gross margin** is ₹125. Match each line to a product, change any price by hand,
and press **Show prices**, then **Draft the quote**.

- The cost comes from the document, and the retailer's quote holds only the price. What
  you paid and your margin stay in this module, kept from custom reports.
- A price below what you paid is refused until you tick that you mean it, and that goes
  in the audit trail.
- The draft is not sent. **Review and send** opens the quote in B2B, where the retailer
  is told by email and accepts it, and accepting makes the order.
- Quoting from a document puts nothing on your supplier accounts, and makes no tax
  invoice.

Prices are before tax, and tax is added as for any B2B order. MRP limits are not
checked. Sending the quote on WhatsApp and a PDF of it are not built.

## Who can do what

| Right | Default | Covers |
|---|---|---|
| `retailassistant.read` | Owner, manager | Seeing bills, suppliers and balances |
| `retailassistant.write` | Owner, manager | Approving bills, recording and allocating money. Booking goods in also needs `inventory.write` |
| `retailassistant.links` | Owner | Which phones may send into the store |

## How a balance is worked out

```
You owe  =  bills posted  −  credit notes  −  verified payments allocated to a bill
Advance  =  verified payments  −  verified payments allocated
```

An unverified payment is shown beside these and counts in neither. A payment is
verified when a person says so, on the payment or when recording it. Undoing money
writes a compensating row with the opposite amount, so the figures can always be
worked back from what was recorded.

## Goods

A bill does not put goods on the shelf. Under **Orders › Suppliers**, open a
supplier and press **Book in goods** on a bill. Each line asks which product it is
and how many arrived; a line whose SKU is exactly a variant's comes matched, and a
whole quantity on the bill comes filled in. Lines left without a product stay open
for later. Behind it, `POST /api/admin/x/retailassistant/invoices/{id}/receive`
moves stock through the stock service once per line, and needs
`retailassistant.write` and `inventory.write`.

If the server dies between recording a receipt and moving the stock, the line stays
`pending`, shows as **Interrupted**, and cannot be received again: a person checks
the stock and corrects it, rather than it being counted twice.

## A walkthrough with no Meta account

Start a store with `.\scripts\dev.ps1 -All`, with a free `-Port` and your own
`-Database`, and these set first. `RETAIL_ASSISTANT_GRAPH_URL` points at a local
stand-in for Meta's API, which you have to run yourself. The tests in
`ext/retailassistant/retailassistant_test.go` carry one to copy (`fakeMeta`).

```powershell
$env:RETAIL_ASSISTANT_APP_SECRET = 'local-secret'
$env:RETAIL_ASSISTANT_VERIFY_TOKEN = 'local-verify'
$env:RETAIL_ASSISTANT_ACCESS_TOKEN = 'local-token'
$env:RETAIL_ASSISTANT_PHONE_NUMBER_ID = '1000'
$env:RETAIL_ASSISTANT_GRAPH_URL = 'http://127.0.0.1:9000'
```

Then:

1. Under **Orders › Supplier bills**, switch it on and add `+91 98000 00001`. Note the code.
2. Send the webhook this body, signed as `X-Hub-Signature-256: sha256=<hex HMAC-SHA256 of the body with local-secret>`, with `LINK` and your code:

   ```json
   {"entry":[{"changes":[{"field":"messages","value":{
     "metadata":{"phone_number_id":"1000"},
     "messages":[{"id":"demo-1","from":"919800000001","type":"text","text":{"body":"LINK 123456"}}]}}]}]}
   ```

3. Send a document the same way, with a new `id` and `"type":"document","document":{"id":"bill-1","filename":"bill.csv"}`. Serve this file from your stand-in as media `bill-1`:

   ```
   Supplier,ABC Cosmetics
   Invoice No,INV-109
   Invoice Date,05/11/2026

   Description,Qty,Rate,Amount
   Lip colour shade 01,10,1000.00,10000.00

   Subtotal,10000.00
   Tax,0.00
   Grand Total,10000.00
   ```

4. It appears under **Supplier bills** within a few seconds. Open it and **Approve and post**.
5. Under **Orders › Suppliers**, ABC Cosmetics is owed ₹10,000.
6. Open it. Record a payment of ₹3,000 with **I know this money was paid** ticked, and **Allocate** it to INV-109. Record a credit note of ₹1,000 and allocate that too. You now owe ₹6,000.
7. Record another payment without the tick. It shows as not verified, and what you owe does not change.
8. Switch the assistant off under **Supplier bills**: the screens say so, and messages to that store are no longer kept. Nothing else in the store changes.

## What has not been tested

- Meta's real webhook, media download and message sending. The request shapes follow Meta's documentation and `ext/notify-whatsapp`.
- AI reading against a real model. Its prompt and answer shape have only met a stand-in.
- Real supplier documents. The reader's labels and columns are a first guess at how Indian bills are laid out, and only real documents will show how far off that is.
- Meta's real template approval and sending. The request follows Meta's documentation.
- The Hindi wording, on WhatsApp and in the panel, with a native speaker.
