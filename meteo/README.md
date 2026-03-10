# Meteo Service

This is a Python Flask service that provides a random temperature for a given city.  

## Features

- REST API endpoint: `/temperature/<city>`  
- Returns JSON with city name and a random temperature.  

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
GET /temperature/<city>
````

Example:

```
http://localhost:5000/temperature/Paris
````

Response:

```json
{
  "city": "Paris",
  "temperature": 23
}
```

## Dependencies

- Flask