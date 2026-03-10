# meteo/app.py
from flask import Flask, jsonify
import random

app = Flask(__name__)

@app.route("/temperature/<city>")
def temperature(city):
    temp = random.randint(-5, 35)
    return jsonify({
        "city": city,
        "temperature": temp
    })

if __name__ == "__main__":
    app.run(host="0.0.0.0", port=5000, debug=False)