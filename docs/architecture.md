# Architecture

```text
Public path proxy :8080
  /studio, /canvas-static -> canvas-web :18100
  /canvas-api             -> canvas-api :18101
  all other paths         -> HAProxy :18080 -> official Sub2API blue/green

canvas-api / canvas-worker
  -> independent sub2api_canvas PostgreSQL
  -> independent object storage
  -> official Sub2API HTTP contracts
```

Canvas uses official Sub2API only for public version discovery, profile
validation, API-key ownership, and standard image generation/editing. It keeps
no official user, password, balance, subscription, account, or billing tables.

An official deployment must leave all Canvas images and state untouched. A
Canvas deployment must leave official images and schema untouched. Contract
tests gate both release streams.

