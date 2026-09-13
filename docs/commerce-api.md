# Products and invoices

All routes use `/api/v1/tenants/:tenantID` and the authenticated bearer session. Trader owners and supporting admins can operate only within their tenant. The super admin explicitly selects a tenant. IDs in request bodies do not bypass tenant checks.

## Units and money

- `quantity` always counts **whole boxes/packs**, not individual pieces. Fractional quantities are rejected.
- `pieces_per_unit` describes the contents of one pack. Ten packs with 12 pieces per pack represent 120 pieces; selling three packs leaves seven packs.
- Product prices are not stored. The trader enters an EGP per-pack price on each invoice. API money fields ending in `_minor` are integer piastres: `2950` means EGP 29.50.
- Pack size: 1–1,000,000. Stock: 0–1,000,000,000 packs. Invoice prices are limited to 0–1,000,000,000,000 piastres. Invoice total: up to 100,000,000,000,000 piastres. Arithmetic is checked before multiplication/addition.
- Invoices are sales on credit in this increment. Posting records the entire total as client debt. Payments, opening balances, tax, discounts, returns and PDF/printing are not yet implemented.

## Products

| Method | Route | Behavior |
| --- | --- | --- |
| GET | `/products` | Paginated list. Optional `q` searches title/code; `active=true` shows only active products. |
| POST | `/products` | Create product and record opening stock; 201. |
| GET | `/products/:id` | Read product; 200. |
| PATCH | `/products/:id` | Update selected fields, including stock; 200. Requires current `version`. |
| DELETE | `/products/:id?version=3` | Archive product; 204. History is retained. |

```json
{
  "title": "Tea pack",
  "code": "TEA-12",
  "quantity": 10,
  "pieces_per_unit": 12
}
```

Creation requires title, code, quantity and pieces_per_unit. Title is 1–150 characters; code is 1–80 and unique within the tenant (case-insensitive under the documented database collation). Responses also include `id`, `tenant_id`, `active`, `version` and `created_at`.

PATCH accepts any combination of the product fields and `active`, plus the required `version`. Example: `{"version":1,"quantity":15}` sets stock to 15 packs and records the delta in stock history. `{"version":2,"active":true}` restores an archived product. A stale version returns 409 rather than overwriting stock changed by another user/invoice. Refresh before retrying.

## Invoice lifecycle

```text
draft --post--> posted --void--> void
  |
  +--cancel--> cancelled
```

Drafts can be edited and do not reserve or deduct stock. Cancelled drafts remain in history. Posted invoices cannot be edited/cancelled/deleted. Voiding reverses the complete invoice once; partial returns are not supported. A void cannot be undone.

| Method | Route | Behavior |
| --- | --- | --- |
| GET | `/invoices` | Paginated, newest first. Optional `status=draft/posted/void/cancelled`. |
| POST | `/invoices` | Create draft; 201. |
| GET | `/invoices/:id` | Read invoice and line snapshots; 200. |
| PUT | `/invoices/:id` | Replace draft contents with a complete body plus `version`; 200. |
| DELETE | `/invoices/:id?version=1` | Cancel draft; returns preserved invoice, 200. |
| POST | `/invoices/:id/post` | Body `{"version":1}`; post draft, 200. |
| POST | `/invoices/:id/void` | Body `{"version":2,"reason":"Order cancelled"}`; void posted invoice, 200. |
| POST | `/invoices/:id/payments` | Record a cash or online payment against a posted invoice; 201. |

Draft creation example:

```json
{
  "client_id": 1,
  "issue_date": "2026-09-12",
  "notes": "Deliver tomorrow",
  "items": [
    {"product_id": 1, "quantity": 3, "unit_price_minor": 2950}
  ]
}
```

`issue_date` is a valid `YYYY-MM-DD` date. Notes are optional (up to 2,000 characters). Include 1–100 distinct products; combine quantities for repeated products. Every line must include `unit_price_minor`; prices are captured in the invoice snapshot and can differ between invoices. All other totals, snapshots, tenant IDs, status and invoice numbers are server-controlled; unknown fields are rejected. Editing requires the same complete body plus the current `version`.

Responses include client/name/address snapshots, immutable line snapshots, `total_minor`, `currency` (`EGP`), `status`, `version`, creator and timestamps. `number` is null for drafts/cancelled drafts. Posting assigns the next tenant-local number, displayed by Flutter as `INV-000001`.

Posting verifies the client/products are active, sufficient stock exists, and each product's pack size still matches its draft snapshot. A pack-size change requires editing/resaving the draft. Historical titles, codes and prices do not change when a product is edited later.

Stock changes, stock-history records, invoice status/number, and client-ledger entries commit in one MySQL transaction. Failed lines roll back the whole transaction. Commerce writes lock the tenant row before affected records, serializing writes within one trader's workspace and preventing overselling. Different tenants remain independent. Voiding restores invoiced packs even if the product has since been archived.

Completed post/void/cancel transitions are idempotent: retrying the same transition returns the existing result without applying stock/debt effects again. Stale draft saves return 409. Creating a draft is not idempotent; after an uncertain create response, refresh the invoice list before creating another draft.

Payments use `{"amount_minor":100000,"method":"cash","notes":""}` or `method:"online"`. Multiple payments are supported, overpayments are rejected, and invoice responses include `paid_minor`, `remaining_minor`, `payment_status` (`unpaid`, `partially_paid`, or `paid`) and `payments`. Financial summaries use the remaining client balance after payments.

## Financial overview

`GET /financial-summary` returns:

```json
{
  "currency": "EGP",
  "receivables_minor": 8850,
  "payables_minor": 0,
  "net_minor": 8850,
  "scope": "posted_invoices_and_voids"
}
```

The ledger is grouped by client before totaling: positive client balances are receivables, negative balances are payables, and net is receivables minus payables. Archived clients remain included. In this increment only posted invoices and their voids populate the ledger, so payables normally remain zero. These figures exclude cash collections, opening balances, stock valuation and profit.

Client lookup also supports `/clients?q=search&active=true&limit=50&offset=0` for the invoice editor. All list endpoints retain the existing pagination format.

## Verification

`MYSQL_COMMERCE_TEST_DSN` enables real-MySQL commerce tests against a **fresh dedicated database**. Tests cover pack arithmetic, exact money, cross-tenant IDs/foreign keys, duplicate codes, stale edits, snapshots, insufficient-stock rollback, competing invoice posts, idempotent transitions, voids and ledger totals. Data is left in the test database for inspection; no business database is modified.

Implementation reference: [MySQL locking reads](https://dev.mysql.com/doc/refman/8.0/en/innodb-locking-reads.html).

## R2 file uploads

R2 is configured only through environment variables: `R2_ACCOUNT_ID`, `R2_BUCKET_NAME`, `R2_ENDPOINT`, `R2_PUBLIC_URL`, `R2_ACCESS_KEY_ID`, and `R2_SECRET_ACCESS_KEY`. The server uses the S3-compatible endpoint and does not store these credentials in MySQL or return them. Keep the bucket private if files contain business documents; `R2_PUBLIC_URL` is only used to populate returned metadata URLs.

| Method | Route | Form field | Behavior |
| --- | --- | --- | --- |
| POST | `/files/upload` | `file` | Upload one file; 201 |
| POST | `/files/uploads` | repeated `files` | Upload 1–20 files; 201 with per-file results |
| GET | `/files` | — | List tenant-owned file metadata |
| DELETE | `/files/:id` | — | Delete the R2 object and metadata; 204 |

Allowed types are JPEG, PNG, WebP and PDF. Each file is 1 byte–50 MiB. Keys are generated as `tenants/{tenant_id}/files/{random-id}{extension}`; original filenames are metadata only. Upload failures clean up objects already uploaded in the same multi-upload request and return an error. Files are currently general tenant files; product/invoice attachment foreign keys can be added once attachment UX is defined.

The multi-upload endpoint accepts up to 20 files. Each file must be between 1 byte and 50 MiB.

Tenant logos use the same R2 storage and are linked to the tenant record. `POST /tenants/:tenantID/logo` accepts one `file` (JPEG, PNG or WebP, up to 50 MiB) and replaces the previous logo. `DELETE /tenants/:tenantID/logo` removes it. Tenant users and super admins can manage logos.

Example:

```sh
curl -X POST http://127.0.0.1:8080/api/v1/tenants/7/files/upload \
  -H "Authorization: Bearer $TOKEN" \
  -F "file=@invoice.pdf"
curl -X POST http://127.0.0.1:8080/api/v1/tenants/7/files/uploads \
  -H "Authorization: Bearer $TOKEN" \
  -F "files=@front.png" -F "files=@back.png"
```
