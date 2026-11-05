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
| Takes files and photos on one shared WhatsApp number | Read PDFs or photos: a person enters those by hand |
| Reads tables in CSV, XLSX and DOCX files | Post anything without a person approving it |
| Keeps each store's records apart, behind the store policy | Keep a file from a number no store has linked |
| Tracks partial payments, advances, credit notes and reversals | Treat a payment screenshot as proof money moved |
| Books goods into stock when somebody says they arrived | Move stock because a bill arrived |

Also not built: barcode checkout, wholesale quotes, insights, WhatsApp sign-in,
OCR or AI reading, a virus scan, retention and deletion rules, encrypting files
at rest, template messages, and bank reconciliation.

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

Under **Orders › Supplier bills**, the owner switches the assistant on and adds a
phone with its country code. The screen shows a code, once. From that phone, send
`LINK 123456` to the shared number. Until then, nothing from that phone is kept.

One phone can be linked to several stores. It then has to say which one first, by
replying `STORE <store's slug>`, and what it sends goes there until it says
otherwise. Nothing is posted into a store by guesswork.

Confirmation messages are off until the owner ticks **Send** beside a phone. They
are free text, so Meta only delivers them inside its customer-service window after
the phone last wrote. Outside that window Meta refuses them, and the refusal is
kept on the message's row. Template messages are not built yet; if you add them,
their wording has to be approved by Meta first.

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

A bill does not put goods on the shelf. `POST /api/admin/x/retailassistant/invoices/{id}/receive`
books a line in, naming the variant and the quantity, and moves stock through the
stock service once per line. There is no screen for this yet. If the server dies
between recording a receipt and moving the stock, the line stays `pending` and
cannot be received again: a person checks the stock and corrects it, rather than
it being counted twice.

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
- Real supplier documents. The reader's labels and columns are a first guess at how Indian bills are laid out, and only real documents will show how far off that is.
- Hindi. The module's wording is in English and is not yet set up for translation.
