# Harbor Take Home Project

Note: this is a fork of [this](https://github.com/harbor-xyz/coding-project) repo that contains the original problem statement

## Stack

### Database

SQLite is chosen to help deploy the application easily. 

### Go

API server is written in Go

## API

Postman collection is exported [here](./go-harbor.postman_collection.json)

## Database design

Design is as follows

![API design](./go-harbor.png)

## Live API

API is deployed to fly (heroku alternative) and the url is [here](https://server-morning-hill-2045.fly.dev/v1/). 
Please checkout postman doc for more info about each API's.

In summary we have
1. `/v1/create-user` 

Body: 

```
{"name": "<name of the user"}
```

2. `/v1/user/set-availability` 

Body: 
```
{"user_id": "<user_id>", "day": <monday, tuesday, wednesday, thursday, friday, saturday, sunday>, "start_time_hour": 14, "start_time_minutes": 0, "end_time_hour": 21, "end_time_minutes": 0}
```

3. `/v1/user/find-available-slots` 

Body: 
```
{"user_id_1": <your user_id>, "user_id_2": <your_peer_user_id>, "date": "2024-07-15", "slot": "14:30", "slot_lookup_config": {"slot_duration": <hourly, half-hourly>, "search_every": <15, 30, 60>}}
```

4. `/v1/user/book-slot` 

Body: 
```
{"user_id_1": <your_user_id>, "user_id_2": <your_peer_user_id>, "date": "2024-07-15", "slot": "14:30", "slot_lookup_config": {"slot_duration": "half-hourly", "search_every": 30}}
```

5. `/v1/user/view-schedule` 

Body:
```
{"user_id": <your_user_id>, "date": "2024-07-15"}
```

Kindly replace the fillers in <> with appropriate data for correct testing

## Expectations -- copied from original problem statement

We care about

- Have you thought through what a good MVP looks like? Does your API support that?
Started off with a basic design to provide a way for user's to
1. Create themselves
2. Set availability similar to google calendar 
3. View their availability for a day
4. Find available slots between their peers
5. Book a slot

The same is implemented in the API

- What trade-offs are you making in your design?

1. Have put a constraint to book either half hourly, one hourly slots to avoid working out different edge cases
2. Scheduling a slot across multiple dates aren't allowed currently but can be supported fairly easily in future if required
3. Data is stored in-memory over an SQLite driver. This can be changed to a regular database
4. User can view the schedule per day. Although we can extend it to support an array of dates if required

- Working code - we should be able to pull and hit the code locally. Bonus points if deployed somewhere.

```
go mod tidy
go run main.go
```

- Any good engineer will make hacks when necessary - what are your hacks and why?
