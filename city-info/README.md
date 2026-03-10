# Web Service

This is a Node.js web service that provides city information and temperature data.  

## Features

- Queries MongoDB for city information (name, location, population).  
- Calls the Meteo service to get the current temperature.  
- Seeds the database on first startup if empty.  

## Usage

1. Ensure MongoDB is running and accessible.  
2. Ensure the Meteo service (`app.py`) is running.  
3. Start the web server:

```bash
node app.js
```

4. Access the API:

```
GET /city?city=name
```

Example:

```
http://localhost:4000/city?city=Paris
````

Response:

```
- Name: London,
- Location: {"lat":51.5072,"lon":-0.1276},
- Population: 8982000,
- Temperature: 29.
```

<img src=screenshot.png>

## Dependencies

- express
- axios
- mongodb