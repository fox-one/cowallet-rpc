# cowallet-rpc

### Auth

直接把 Oauth keystore json 后放在 Header Authorization 里面

### get vault

```http request
GET /vaults?members=1&members=2&members=3&threshold=2
```

**Response**

```json5
{
  "members": ["1", "2", "3"],
  "threshold": 2,
  "updated_at": "2021-08-10T07:00:00Z",
  "assets": [
    {
      "id": "43d61dcd-9a3f-3b5c-9f0f-7b4d3f3e6f6d",
      "hash": "43d61dcd9a3f3b5c9f0f7b4d3f3e6f6d",
      "balance": "12",
      "unspent": "10",
      "signed": "2",
      "requests": []
    }
  ]
}
```

### list snapshots

```http request
GET /snapshots?members=1&members=2&members=3&threshold=2&offset=2021-08-10T07:00:00Z&limit=10
```

**Response**

```json5
[
  {
    "id": "f6ff4cfa-3761-4bd2-b196-dddccf4845fd",
    "created_at": "2021-08-10T07:00:00Z",
    "asset_id": "54c61a72-b982-4034-a556-0d99e3c21e39",
    "amount": "100",
    "memo": "foo"
  }
]
```
