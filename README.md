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

### list requests

```http request
GET /request?members=1&members=2&members=3&threshold=2&offset=2021-08-10T07:00:00Z&limit=10
```

**Response**

```json5
[
  {
    "type": "transaction_request",
    "request_id": "956b960f-1905-4609-991c-428b87e6aed6",
    "transaction_hash": "4a81d69b6e8e095e5a1671771c314d803b9810d3c5dddaeaab2bf6a80455dfe5",
    "asset_id": "965e5c6e-434c-3fa9-b780-c50f43cd955c",
    "kernel_asset_id": "b9f49cf777dc4d03bc54cd1367eebca319f8603ea1ce18910d09e2c540c630d8",
    "amount": "1",
    "senders_hash": "8481857dd3552fbbb233cadf9e051c2da9a40c164420b759e1606f0f98d68ae5",
    "senders_threshold": 1,
    "senders": [
      "324a05bf-6b1d-3234-9a7e-283cb025c122"
    ],
    "signers": [
      "324a05bf-6b1d-3234-9a7e-283cb025c122"
    ],
    "extra": "",
    "raw_transaction": "77770005b9f49cf777dc4d03bc54cd1367eebca319f8603ea1ce18910d09e2c540c630d80001deb61617b968c172fb2df4c05d3ada3e5786a327c69aefcd891b807b8873ffce000100000000000000020000000405f5e1000001ef49cf0b2480e1fc004cb20a55efc170260a3b0811d4e5766471b52a7194deecc7b188247103491326c322a994f355944c60759faf41f321651da2f4aa47a8c80003fffe010000000000040bebc1fb00019657c4204f7b713dcd0447dd1191a8cda4f1575d46c742f746799cdbe762ba207bc3fb52298f07ae052ba54f49262a408e628f107a0ab81f1c319e16adec65430003fffe0100000000000000000000",
    "created_at": "2024-01-17T03:03:02.898299Z",
    "updated_at": "2024-01-17T03:03:03.117735Z",
    "receivers": [
      {
        "members": [
          "5f3d5b3d-383a-482a-80a0-f7c879ed8ced"
        ],
        "members_hash": "582428e7ba1132da60f1932db8f71050632c0d32d538332bc1e7823fab9591e0",
        "threshold": 1,
        "destination": "",
        "tag": "",
        "withdrawal_hash": ""
      },
      {
        "members": [
          "324a05bf-6b1d-3234-9a7e-283cb025c122"
        ],
        "members_hash": "8481857dd3552fbbb233cadf9e051c2da9a40c164420b759e1606f0f98d68ae5",
        "threshold": 1,
        "destination": "",
        "tag": "",
        "withdrawal_hash": ""
      }
    ]
  }
]
```