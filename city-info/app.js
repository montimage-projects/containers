const express = require("express");
const axios = require("axios");
const { MongoClient } = require("mongodb");

const app = express();
const PORT = 4000;

const mongoUri = "mongodb://mongodb:27017";
const client = new MongoClient(mongoUri);

let citiesCollection;

async function seedDatabase() {
	const count = await citiesCollection.countDocuments();

	if (count === 0) {
		console.log("Seeding cities collection...");

		await citiesCollection.insertMany([
			{ name: "Paris", location: { lat: 48.8566, lon: 2.3522 }, population: 2148000 },
			{ name: "London", location: { lat: 51.5072, lon: -0.1276 }, population: 8982000 },
			{ name: "Berlin", location: { lat: 52.52, lon: 13.405 }, population: 3769000 }
		]);

		console.log("Seeding completed");
	} else {
		console.log("Cities already exist, skipping seed");
	}
}

async function init() {
	console.log("Connecting to MongoDB", mongoUri);
	await client.connect();

	const db = client.db("citiesdb");
	citiesCollection = db.collection("cities");

	await seedDatabase();
}

app.get("/", async (req, res) => {
	res.send(`<center>
		<h1>Welcome to City info!</h1>
		<form action=/city method=GET target=data>
		Select City: 
			<select name=city>
				<option name=Paris>Paris</option>
				<option name=London>London</option>
				<option name=Berlin>Berlin</option>
			</select>
		<input type=Submit value=Send>
		</form>
		<iframe name=data width="400" height="200" frameborder="0"></iframe>
	</center>`
	);
});

app.get("/city", async (req, res) => {
	const cityName = req.query.city;

	try {
		const city = await citiesCollection.findOne({ name: cityName });

		if (!city) 
			return res.status(404).end(`Error: City ${city} not found!`);

		const meteo = await axios.get(`http://meteo:5000/temperature/${cityName}`);

		res.send(
		`
			- Name:       ${city.name},<br/>
			- Location:   ${JSON.stringify(city.location)},<br/>
			- Population: ${city.population},<br/>
			- Temperature: ${meteo.data.temperature}.
		`);

	} catch (err) {
		console.error(err);
		res.status(500).send("Internal error");
	}
});


//catches ctrl+c event
process.on('SIGINT', ()=>{
	console.log("bye!");
	process.exit();
});


init().then(() => {
	app.listen(PORT, () => {
		console.log(`Web server running on port ${PORT}`);
	});
});