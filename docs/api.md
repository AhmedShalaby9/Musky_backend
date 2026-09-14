# API v1

Base URL: `http://127.0.0.1:8080/api/v1`. JSON requests require `Content-Type: application/json`. All endpoints except login require `Authorization: Bearer <access_token>`. Unknown JSON fields are rejected. Bodies are limited to 64 KiB. Dates are UTC.

Errors use `{"error":"message"}`. Invalid input: 400; missing/expired authentication: 401; denied role: 403; missing or out-of-tenant resource: 404; duplicate email or trader-owner removal: 409; wrong content type: 415; login throttling: 429. Unexpected database errors do not expose SQL or credentials.

List endpoints accept `limit=1..100` (default 50) and nonnegative `offset` (default 0), returning `{"data":[],"limit":50,"offset":0}` ordered by ID. Lists include inactive records, with their `active` flags, so desktop administration can restore them.

## Authentication

| Method | Path | Body / result |
| --- | --- | --- |
| POST | `/auth/login` | `{"email":"owner@example.com","password":"your password"}`; returns token, expiry and user |
| GET | `/me` | Current user's `id`, `tenant_id` (null for super admin), `name`, `email`, `role`, `active`, `created_at` |
| POST | `/auth/logout` | No body; revokes current session; 204 |
| PUT | `/me/password` | `{"current_password":"old password","new_password":"new password"}`; revokes all sessions; 204; log in again |

Passwords must be 12–72 UTF-8 bytes. Emails are trimmed, lowercased and globally unique. Passwords/hashes are never returned. Tokens are random opaque strings, not JWTs. The desktop client must securely store a token and return to login after 401.

## Tenant administration (super admin)

| Method | Path | Behavior |
| --- | --- | --- |
| GET | `/tenants` | Paginated businesses |
| POST | `/tenants` | Create separate tenant and trader owner atomically; 201 |
| PATCH | `/tenants/:tenantID` | Change `name` and/or `active`; 200. Deactivation revokes tenant sessions. |

Create example:

```json
{
  "name": "Musky Trading",
  "trader": {
    "name": "Ahmed",
    "email": "ahmed@example.com",
    "password": "replace-with-a-unique-password"
  }
}
```

Returns `{"id":1,"name":"Musky Trading","active":true,"trader_id":2}`. The owner role is fixed to `trader`. Tenant/trader names are required and limited to 150 characters.

## Users

Prefix: `/tenants/:tenantID/users`. Super admins may select a tenant; traders and supporting admins may only use their own. All can list/read tenant users. Only trader owners and the super admin can create/update/deactivate supporting admins.

| Method | Path suffix | Behavior |
| --- | --- | --- |
| GET | empty | List tenant users |
| POST | empty | Create supporting admin; 201 |
| GET | `/:id` | Get tenant user |
| PATCH | `/:id` | Update allowed fields; 200 |
| DELETE | `/:id` | Deactivate user and revoke sessions; 204 |

Creation body:

```json
{"name":"Supporting Admin","email":"admin@example.com","password":"replace-with-a-unique-password","role":"admin"}
```

PATCH accepts any nonempty combination of `name`, `email`, `password`, `role`, `active`. It never accepts `tenant_id` or `id`. Only trader owners and the super admin may create/manage supporting admins. New traders must be created through `POST /tenants` so each gets a separate tenant. Only the super admin may edit a trader account; the trader changes their own password through `/me/password`. No API permits a `super_admin` role assignment or changes between trader/admin roles. A trader owner cannot be deactivated or demoted; suspend the tenant instead. Changing password, role, email or active status revokes sessions. Restore using `{"active":true}`.

## Clients

Prefix: `/tenants/:tenantID/clients`. All authenticated roles can read/create/edit within an authorized tenant. Clients are shared within a tenant, not limited to their responsible user.

| Method | Path suffix | Behavior |
| --- | --- | --- |
| GET | empty | List clients |
| POST | empty | Create client; 201 |
| GET | `/:id` | Get client |
| PATCH | `/:id` | Update supplied fields; 200 |
| DELETE | `/:id` | Delete client; 204. Returns 409 if referenced by invoices or payments |

Creation example:

```json
{
  "name": "Client One",
  "phone": "01000000000",
  "address": "Cairo"
}
```

`name` is required (1–150 characters). Optional fields: `phone` (max 40, nullable), `address` (500), `user_id`, `active`. `user_id` defaults to the authenticated tenant user; the super admin must specify an active tenant user explicitly. The trader and supporting admins may assign/reassign to another active user in the same tenant and set `active` (archive/restore). PATCH supports the same fields, all optional, and requires at least one supplied value. Clear optional text with an empty string or `null`.

Responses contain `id`, `tenant_id`, `user_id`, the contact fields, `active` and `created_at`. Clients never log in. Product, invoice, stock and financial-summary endpoints are documented in [commerce-api.md](commerce-api.md).
