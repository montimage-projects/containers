# events/app.py
from flask import Flask, jsonify
import sys
import random

app = Flask(__name__)

# Dummy events
events = [
    {
        "name": "Concert Jazz",
        "description": "Live jazz concert in the city hall",
        "horraire": "2026-03-15 19:00",
        "address": "123 Main St, Cityville"
    },
    {
        "name": "Art Exhibition",
        "description": "Modern art gallery opening",
        "horraire": "2026-03-18 10:00",
        "address": "45 Art Blvd, Cityville"
    },
    {
        "name": "Food Festival",
        "description": "Taste local and international cuisines",
        "horraire": "2026-03-20 12:00",
        "address": "Central Park, Cityville"
    }
]

@app.route("/event/<city>", methods=["GET"])
def get_random_event(city):
    event = random.choice(events)
    return jsonify({"event": event, "city": city})

if __name__ == "__main__":
    port = 5001  # default port
    if len(sys.argv) > 1:
        try:
            port = int(sys.argv[1])
        except ValueError:
            print("Invalid port number, using default 5001")

    app.run(host="0.0.0.0", port=port, debug=False)