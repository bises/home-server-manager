# Simple Python API

A simple Flask API with one GET endpoint.

## Setup

1. Install dependencies:

```bash
pip install -r requirements.txt
```

2. Run the API:

```bash
python app.py
```

## Endpoint

- **GET** `/api/hello` - Returns a greeting message

Example response:

```json
{
  "message": "Hello, World!",
  "status": "success"
}
```

The API runs on `http://localhost:5000`
