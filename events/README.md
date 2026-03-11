# Meteo Service

This is a Python Flask service that provides a random culture event for a given city.  

## Features

- REST API endpoint: `/event/<city>`  
- Returns JSON with city name and a random event.  

## Usage

1. Install dependencies:

```bash
pip install -r requirements.txt
```

2. Start the service:

```bash
python app.py
```

3. Access the API:

```
GET /event/<city>
````

Example:

```
http://localhost:5001/event/Paris
````

Response:

```json
{
  "name": "Food Festival",
  "description": "Taste local and international cuisines",
  "horraire": "2026-03-20 12:00",
  "address": "Central Park, Cityville",
  "city": "Paris"
}
```

## Dependencies

- Flask