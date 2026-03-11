# meteo/app.py
from flask import Flask, jsonify
import random
import sys

app = Flask(__name__)

@app.route("/temperature/<city>")
def temperature(city):
    temp = random.randint(-5, 35)
    return jsonify({
        "city": city,
        "temperature": temp
    })

if __name__ == "__main__":
    port = 5000

    if len(sys.argv) > 1:
        try:
            port = int(sys.argv[1])
        except ValueError:
            print("Invalid port number, using default 5000")

    app.run(host="0.0.0.0", port=port, debug=False)