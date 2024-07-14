package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	_ "github.com/mattn/go-sqlite3"
)

// typedefs
type availabilityStatus map[string]bool
type availabilityInfo map[string]userAvailability

var dayOfTheWeekMap = make(map[time.Weekday]string)

type slotDuration string

const (
	halfHourly slotDuration = "half-hourly"
	hourly     slotDuration = "hourly"
)

var file string = fmt.Sprintf("%s?%s", "file:calendar.db", "_foreign_keys=on")

var durationToInt = map[slotDuration]int{
	halfHourly: 30,
	hourly:     60,
}

// SQL statements DDL, DML
const userCreate string = `
CREATE TABLE IF NOT EXISTS calendar_user (
	id INTEGER NOT NULL PRIMARY KEY,
	name VARCHAR(20) NOT NULL,
	created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
`

const availabilityCreate string = `
CREATE TABLE IF NOT EXISTS calendar_user_availability (
  user_id INTEGER NOT NULL,
	day TEXT CHECK (day IN ('monday', 'tuesday', 'wednesday', 'thursday', 'friday', 'saturday', 'sunday')) NOT NULL,
	start_time_hour INTEGER CHECK (start_time_hour > 0 AND start_time_hour < 24) NOT NULL,
	start_time_minutes INTEGER CHECK (start_time_minutes >= 0 AND start_time_minutes < 60) NOT NULL,
	end_time_hour INTEGER CHECK (end_time_hour >= 0 AND end_time_hour < 24) NOT NULL,
	end_time_minutes INTEGER CHECK (end_time_minutes >= 0 AND end_time_minutes < 60) NOT NULL,
	created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
	PRIMARY KEY (user_id, day),
	FOREIGN KEY (user_id) REFERENCES calendar_user(id)
)`

const bookedSlots string = `
CREATE TABLE IF NOT EXISTS calendar_user_booked_slots (
	id INTEGER NOT NULL PRIMARY KEY,
  user_id_1 INTEGER NOT NULL,
  user_id_2 INTEGER NOT NULL,
	date DATE NOT NULL,
	start_time_hour INTEGER CHECK (start_time_hour > 0 AND start_time_hour < 24) NOT NULL,
	start_time_minutes INTEGER CHECK (start_time_minutes >= 0 AND start_time_minutes < 60) NOT NULL,
	end_time_hour INTEGER CHECK (end_time_hour >= 0 AND end_time_hour < 24) NOT NULL,
	end_time_minutes INTEGER CHECK (end_time_minutes >= 0 AND end_time_minutes < 60) NOT NULL,
	slot_type TEXT CHECK (slot_type IN ('hourly', 'half-hourly')) NOT NULL,
	created_at DATETIME DEFAULT CURRENT_TIMESTAMP,

	FOREIGN KEY (user_id_1) REFERENCES calendar_user(id),
	FOREIGN KEY (user_id_2) REFERENCES calendar_user(id)
)`

const insertUser string = `
INSERT INTO calendar_user (name) VALUES (?);
`

const insertAvailability string = `
INSERT INTO calendar_user_availability (user_id, day, start_time_hour, start_time_minutes, end_time_hour, end_time_minutes) VALUES (?, ?, ?, ?, ?, ?);`

const getUserAvailabilitySetting string = `
SELECT user_id, start_time_hour, start_time_minutes, end_time_hour, end_time_minutes FROM calendar_user_availability WHERE user_id=? AND day=?;`

const getUserBookedSlots string = `
SELECT user_id_1, user_id_2, date, start_time_hour, start_time_minutes, end_time_hour, end_time_minutes, slot_type  FROM calendar_user_booked_slots WHERE (user_id_1=? OR user_id_1=?) AND (user_id_2=? OR user_id_2=?) AND date=?;`

const getSingleUserBookedSlots string = `
SELECT user_id_1, user_id_2, date, start_time_hour, start_time_minutes, end_time_hour, end_time_minutes, slot_type  FROM calendar_user_booked_slots WHERE (user_id_1=? OR user_id_2=?) AND date=?;`

const insertSlot string = `
INSERT INTO calendar_user_booked_slots (user_id_1, user_id_2, date, start_time_hour, start_time_minutes, end_time_hour, end_time_minutes, slot_type) VALUES (?, ?, ?, ?, ?, ?, ?, ?);`

type server struct {
	mu sync.Mutex
	db *sql.DB
}

// data structures to capture business data
type scheduledSlot struct {
	UserID1          int          `json:"user_id_1"`
	UserID2          int          `json:"user_id_2"`
	Date             string       `json:"date"`
	StartTimeHour    int          `json:"start_time_hour"`
	StartTimeMinutes int          `json:"start_time_minutes"`
	EndTimeHour      int          `json:"end_time_hour"`
	EndTimeMinutes   int          `json:"end_time_minutes"`
	SlotDuration     slotDuration `json:"slot_duration"`
}

type userSlot struct {
	UserID           int    `json:"user_id"`
	Date             string `json:"date"`
	StartTimeHour    int    `json:"start_time_hour"`
	StartTimeMinutes int    `json:"start_time_minutes"`
	EndTimeHour      int    `json:"end_time_hour"`
	EndTimeMinutes   int    `json:"end_time_minutes"`
}

type userAvailability struct {
	UserId           int `json:"user_id"`
	StartTimeHour    int `json:"start_time_hour"`
	StartTimeMinutes int `json:"start_time_minutes"`
	EndTimeHour      int `json:"end_time_hour"`
	EndTimeMinutes   int `json:"end_time_minutes"`
}

type slotInput struct {
	UserID1    int        `json:"user_id_1"`
	UserID2    int        `json:"user_id_2"`
	Date       string     `json:"date"`
	SlotConfig slotConfig `json:"slot_lookup_config"`
}

// TOdo: date is < current date
type viewScheduleInput struct {
	UserID int    `json:"user_id"`
	Date   string `json:"date"`
}

type findAvailableSlotInput struct {
	slotInput
}

// Check the available virtual slots that can be claimed on both the users
type bookSlotInput struct {
	Slot string `json:"slot"`
	slotInput
}

type slotConfig struct {
	SlotDuration slotDuration `json:"slot_duration"`
	Every        int          `json:"search_every"` // TODO: validate it to be > 20 and <= 60
}

type availabilityInput struct {
	UserID           string `json:"user_id"`
	Day              string `json:"day"`
	StartTimeHour    int    `json:"start_time_hour"`
	StartTimeMinutes int    `json:"start_time_minutes"`
	EndTimeHour      int    `json:"end_time_hour"`
	EndTimeMinutes   int    `json:"end_time_minutes"`
}

type userInput struct {
	Name string `json:"name"`
}

// end of business logic types

func createUser(c *gin.Context) {
	jsonData, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"status":  "error",
			"message": "invalid request payload",
		})
		return
	}
	var user userInput
	err = json.Unmarshal(jsonData, &user)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"status":  "error",
			"message": "invalid request payload",
		})
		return
	}
	if len(user.Name) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"status":  "error",
			"message": "user name is empty",
		})
		return
	}
	// Create an insert statement to be executed on the database
	db, err := sql.Open("sqlite3", file)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"status":  "error",
			"message": "internal error",
		})
		return
	}
	defer db.Close()
	res, err := db.Exec(insertUser, user.Name)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"status":  "error",
			"message": "unable to create user",
		})
		return
	}
	var id int64
	if id, err = res.LastInsertId(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"status":  "error",
			"message": "unable to create user",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"id": id,
	})
}

// viewSchedule will allow users to view availability on a particular day
func viewSchedule(c *gin.Context) {
	jsonData, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"status":  "error",
			"message": "invalid request body",
		})
		return
	}
	var viewSchedule viewScheduleInput
	err = json.Unmarshal(jsonData, &viewSchedule)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"status":  "error",
			"message": "invalid request body",
		})
		return
	}

	// Parse the date
	layout := "2006-01-02"
	t, err := time.Parse(layout, viewSchedule.Date)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"status":  "error",
			"message": "invalid date format, kindly format the date to yyyy-mm-dd format",
		})
		return
	}
	dayOfTheWeek := dayOfTheWeekMap[t.Weekday()]

	// Create an insert statement to be executed on the database
	db, err := sql.Open("sqlite3", file)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"status":  "error",
			"message": "something went wrong",
		})
		return
	}
	defer db.Close()
	user, err := getUserAvailability(viewSchedule.UserID, dayOfTheWeek)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"status":  "error",
			"message": "unable to get availability",
		})
		return
	}
	userSlot := make(map[int]availabilityStatus)
	userSlotInfo := make(map[int]availabilityInfo)

	// tip: this function update's the map by reference
	err = buildSlotAvailability(user, &userSlot, &userSlotInfo, hourly, 60)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"status":  "error",
			"message": "unable to get availability",
		})
		return
	}
	bookedSlots, err := db.Query(getSingleUserBookedSlots, viewSchedule.UserID, viewSchedule.UserID, viewSchedule.Date)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"status":  "error",
			"message": "unable to get booked slots",
		})
		return
	}

	for bookedSlots.Next() {
		var slot scheduledSlot
		if err := bookedSlots.Scan(&slot.UserID1, &slot.UserID2, &slot.Date, &slot.StartTimeHour, &slot.StartTimeMinutes, &slot.EndTimeHour, &slot.EndTimeMinutes, &slot.SlotDuration); err != nil {
			fmt.Println(err)
			// TODO: handle error
		}
		if slot.UserID1 == viewSchedule.UserID {
			userSlot[slot.UserID1][fmt.Sprintf("%02d:%02d", slot.StartTimeHour, slot.StartTimeMinutes)] = false
		}
		if slot.UserID2 == viewSchedule.UserID {
			userSlot[slot.UserID2][fmt.Sprintf("%02d:%02d", slot.StartTimeHour, slot.StartTimeMinutes)] = false
		}
	}
	userSlots := userSlot[viewSchedule.UserID]
	var availableSlots = []string{}
	var bs = []string{}
	for k, v := range userSlots {
		if !v {
			bs = append(bs, k)
		} else {
			availableSlots = append(availableSlots, k)
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"date":            viewSchedule.Date,
		"booked_slots":    bs,
		"available_slots": availableSlots,
	})
}

// setAvailability will allow users to set availability on a particular day
// of the week
func setAvailability(c *gin.Context) {
	jsonData, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"status":  "error",
			"message": "invalid request payload",
		})
		return
	}
	var availability availabilityInput
	err = json.Unmarshal(jsonData, &availability)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"status":  "error",
			"message": "invalid request payload",
		})
		return
	}
	startHourMinute, err := mergeToHourMinute(availability.StartTimeHour, availability.StartTimeMinutes)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"status":  "error",
			"message": "unable to parse start time",
		})
		return
	}
	endHourMinute, err := mergeToHourMinute(availability.EndTimeHour, availability.EndTimeMinutes)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"status":  "error",
			"message": "unable to parse end time",
		})
		return
	}
	if startHourMinute >= endHourMinute {
		c.JSON(http.StatusBadRequest, gin.H{
			"status":  "error",
			"message": "start and end time cannot be equal or greater than end time",
		})
		return
	}
	// Create an insert statement to be executed on the database
	db, err := sql.Open("sqlite3", file)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"status":  "error",
			"message": "internal error",
		})
		return
	}
	defer db.Close()

	res, err := db.Exec(insertAvailability, availability.UserID, availability.Day, availability.StartTimeHour, availability.StartTimeMinutes, availability.EndTimeHour, availability.EndTimeMinutes)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"status":  "error",
			"message": "unable to set availability",
		})
		return
	}
	var id int64
	if id, err = res.RowsAffected(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"status":  "error",
			"message": err.Error(),
		})
		return
	}
	if id < 1 {
		c.JSON(http.StatusBadRequest, gin.H{
			"status":  "error",
			"message": "something went wrong",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status": "success",
	})
}

// bookSlot invokes
// build in function to identify calendar diff for
// user 1 and user 2
// returns slot's that are available on a given day
// considering the already booked slots
func bookSlot(c *gin.Context) {
	jsonData, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"status":  "error",
			"message": "invalid request body",
		})
		return
	}
	var bookInput bookSlotInput
	err = json.Unmarshal(jsonData, &bookInput)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"status":  "error",
			"message": "invalid request body",
		})
		return
	}

	// Parse the date
	layout := "2006-01-02"
	t, err := time.Parse(layout, bookInput.Date)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"status":  "error",
			"message": "invalid date format, kindly format the date to yyyy-mm-dd format",
		})
		return
	}

	dayOfTheWeek := dayOfTheWeekMap[t.Weekday()]
	if bookInput.SlotConfig.Every < 15 || bookInput.SlotConfig.Every > 60 {
		c.JSON(http.StatusBadRequest, gin.H{
			"status":  "error",
			"message": "minumum search interval is 15 mins and max is 60 minutes period",
		})
		return
	}

	userSlotPtr, userSlotMapPtr, err := getSlotDiffs(slotInput{
		UserID1:    bookInput.UserID1,
		UserID2:    bookInput.UserID2,
		Date:       bookInput.Date,
		SlotConfig: bookInput.SlotConfig,
	}, dayOfTheWeek, bookInput.SlotConfig.Every)

	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message": "something went wrong",
		})
		return
	}

	userSlot := *userSlotPtr
	userSlotInfo := *userSlotMapPtr

	// Now for every slot in user 1, find out if the same slot is available in user 2 or not and append and return
	user1Slots := userSlot[bookInput.UserID1]
	user2Slots := userSlot[bookInput.UserID2]
	// var availableSlots []string
	availableSlotsMap := make(map[string]bool)
	for slot, availability := range user1Slots {
		if availability {
			if user2Slots[slot] {
				// availableSlots = append(availableSlots, slot)
				availableSlotsMap[slot] = true
			}
		}
	}

	// Create an insert statement to be executed on the database
	db, err := sql.Open("sqlite3", file)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"status":  "error",
			"message": "something went wrong",
		})
		return
	}
	defer db.Close()

	if !availableSlotsMap[bookInput.Slot] {
		c.JSON(http.StatusBadRequest, gin.H{
			"status":  "error",
			"message": "slot unavailable",
		})
		return
	}
	// insert
	user1 := bookInput.UserID1
	user2 := bookInput.UserID2
	slot := bookInput.Slot
	slotResponse, err := db.Exec(insertSlot,
		user1,
		user2,
		bookInput.Date,
		userSlotInfo[user1][slot].StartTimeHour,
		userSlotInfo[user1][slot].StartTimeMinutes,
		userSlotInfo[user1][slot].EndTimeHour,
		userSlotInfo[user1][slot].EndTimeMinutes,
		bookInput.SlotConfig.SlotDuration,
	)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"status":  "error",
			"message": "unable to confirm the slot",
		})
		return
	}
	var id int64
	if id, err = slotResponse.RowsAffected(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"status":  "error",
			"message": err.Error(),
		})
		return
	}
	if id < 1 {
		c.JSON(http.StatusBadRequest, gin.H{
			"status":  "error",
			"message": "something went wrong",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status": "success",
	})
}

// findAvailableSlots invokes
// build in function to identify calendar diff for
// user 1 and user 2
// returns slot's that are available on a given day
// considering the already booked slots
func findAvailableSlots(c *gin.Context) {
	jsonData, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"status":  "error",
			"message": "invalid request body",
		})
		return
	}
	var findSlotInput findAvailableSlotInput
	err = json.Unmarshal(jsonData, &findSlotInput)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"status":  "error",
			"message": "invalid request body",
		})
		return
	}

	// Parse the date
	layout := "2006-01-02"
	t, err := time.Parse(layout, findSlotInput.Date)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"status":  "error",
			"message": "invalid date format, kindly format the date to yyyy-mm-dd format",
		})
		return
	}

	dayOfTheWeek := dayOfTheWeekMap[t.Weekday()]

	if findSlotInput.SlotConfig.Every < 15 || findSlotInput.SlotConfig.Every > 60 {
		c.JSON(http.StatusBadRequest, gin.H{
			"status":  "error",
			"message": "minumum search interval is 15 mins and max is 60 minutes period",
		})
		return
	}

	userSlotPtr, _, err := getSlotDiffs(slotInput{
		UserID1:    findSlotInput.UserID1,
		UserID2:    findSlotInput.UserID2,
		Date:       findSlotInput.Date,
		SlotConfig: findSlotInput.SlotConfig,
	}, dayOfTheWeek, findSlotInput.SlotConfig.Every)

	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"status":  "error",
			"message": "something went wrong",
		})
		return
	}

	userSlot := *userSlotPtr
	// userSlotInfo := *userSlotMapPtr

	// Now for every slot in user 1, find out if the same slot is available in user 2 or not and append and return
	user1Slots := userSlot[findSlotInput.UserID1]
	user2Slots := userSlot[findSlotInput.UserID2]
	var availableSlots []string
	availableSlotsMap := make(map[string]bool)
	for slot, availability := range user1Slots {
		if availability {
			if user2Slots[slot] {
				availableSlots = append(availableSlots, slot)
				availableSlotsMap[slot] = true
			}
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"date":  findSlotInput.Date,
		"slots": availableSlots,
	})
}

// retrieves availability set by user on a given weekday
// // simple lookup against database
func getUserAvailability(user int, dayOfTheWeek string) (userAvailability, error) {
	var userAvailabilityStatus = userAvailability{}
	db, err := sql.Open("sqlite3", file)
	if err != nil {
		return userAvailabilityStatus, errors.New("error opening a database connection")
	}
	defer db.Close()

	res := db.QueryRow(getUserAvailabilitySetting, user, dayOfTheWeek)

	if err = res.Scan(&userAvailabilityStatus.UserId, &userAvailabilityStatus.StartTimeHour, &userAvailabilityStatus.StartTimeMinutes, &userAvailabilityStatus.EndTimeHour, &userAvailabilityStatus.EndTimeMinutes); err == sql.ErrNoRows {
		return userAvailabilityStatus, err
	}
	return userAvailabilityStatus, nil
}

// utility
func mergeToHourMinute(hour int, minute int) (int, error) {
	hourMinuteStr := fmt.Sprintf("%02d%02d", hour, minute)
	hourMinute, err := strconv.Atoi(hourMinuteStr)
	if err != nil {
		return 0, err
	}
	return hourMinute, nil
}
func splitHourMinute(hourMinute int) (hour int, minute int) {
	hour = hourMinute / 100
	minute = hourMinute % 100
	return hour, minute
}

func addToHourMinute(hourMinute int, duration int) (int, error) {
	hour := hourMinute / 100
	minute := (hourMinute % 100) + duration

	if minute >= 60 {
		hour += 1
		minute = minute % 60
	}
	return mergeToHourMinute(hour, minute)
}

// end of utility

// buildSlotAvailability builds a map with key representing hh:mm formatted slot in the
// provided interval (hourly, half-hourly)
func buildSlotAvailability(user userAvailability, userSlot *map[int]availabilityStatus, userSlotInfo *map[int]availabilityInfo, requestedDuration slotDuration, every int) error {
	startHourMinute, err := mergeToHourMinute(user.StartTimeHour, user.StartTimeMinutes)
	if err != nil {
		return err
	}

	endHourMinute, err := mergeToHourMinute(user.EndTimeHour, user.EndTimeMinutes)

	if err != nil {
		return err
	}

	duration := durationToInt[requestedDuration]

	for startHourMinute < endHourMinute {
		// Check if user map exists
		if _, ok := (*userSlot)[user.UserId]; !ok {
			(*userSlot)[user.UserId] = make(map[string]bool)
			(*userSlotInfo)[user.UserId] = make(map[string]userAvailability)
		}
		startHour, startMinute := splitHourMinute(startHourMinute)
		// userSlot[user1.UserId][fmt.Sprintf("%d:%d-%d:%d", startHourUser1, user1.StartTimeMinutes, startHourUser1, ((user2.StartTimeMinutes+60)-1))] = true
		// Check if we can schedule a 1 hour slot from start time
		startHourMinuteChecker, err := addToHourMinute(startHourMinute, duration)
		if err != nil {
			return err
		}

		if startHourMinuteChecker > endHourMinute {
			break
		}
		endMinuteComputer, err := addToHourMinute(startHourMinute, duration)
		if err != nil {
			return err
		}
		endHour, endMinute := splitHourMinute(endMinuteComputer)
		if endMinute == 0 {
			endMinute = 60
			endHour -= 1
		}

		(*userSlot)[user.UserId][fmt.Sprintf("%02d:%02d", startHour, startMinute)] = true
		(*userSlotInfo)[user.UserId][fmt.Sprintf("%02d:%02d", startHour, startMinute)] = userAvailability{
			StartTimeHour:    startHour,
			StartTimeMinutes: startMinute,
			EndTimeHour:      endHour,
			EndTimeMinutes:   endMinute - 1,
		}
		// But do this every `every`
		startHourMinute, err = addToHourMinute(startHourMinute, every)
		if err != nil {
			return err
		}
	}
	return nil
}

func getSlotDiffs(input slotInput, dayOfTheWeek string, every int) (*map[int]availabilityStatus, *map[int]availabilityInfo, error) {
	// Create an insert statement to be executed on the database
	db, err := sql.Open("sqlite3", file)
	if err != nil {
		return nil, nil, err
	}
	defer db.Close()
	user1, err := getUserAvailability(input.UserID1, dayOfTheWeek)
	if err != nil {
		return nil, nil, errors.New("no availability set for user 1")
	}
	user2, err := getUserAvailability(input.UserID2, dayOfTheWeek)
	if err != nil {
		return nil, nil, errors.New("no availability set for user 2")
	}

	requestedSlotDuration := input.SlotConfig.SlotDuration

	userSlot := make(map[int]availabilityStatus)
	userSlotInfo := make(map[int]availabilityInfo)

	// tip: this function update's the map by reference
	err = buildSlotAvailability(user1, &userSlot, &userSlotInfo, requestedSlotDuration, every)
	if err != nil {
		return nil, nil, err
	}
	err = buildSlotAvailability(user2, &userSlot, &userSlotInfo, requestedSlotDuration, every)
	if err != nil {
		return nil, nil, err
	}

	// fmt.Println(userSlot)
	// fmt.Println(userSlotInfo)

	bookedSlots, err := db.Query(getUserBookedSlots, input.UserID1, input.UserID2, input.UserID1, input.UserID2, input.Date)
	if err != nil {
		return nil, nil, err
	}

	// go through the list of booked slots for
	// user_1 and user_2,
	// mark the slots booked on either side to be available as false
	for bookedSlots.Next() {
		var slot scheduledSlot
		if err := bookedSlots.Scan(&slot.UserID1, &slot.UserID2, &slot.Date, &slot.StartTimeHour, &slot.StartTimeMinutes, &slot.EndTimeHour, &slot.EndTimeMinutes, &slot.SlotDuration); err != nil {
			// TODO: handle error
		}
		userSlot[slot.UserID1][fmt.Sprintf("%02d:%02d", slot.StartTimeHour, slot.StartTimeMinutes)] = false
		userSlot[slot.UserID2][fmt.Sprintf("%02d:%02d", slot.StartTimeHour, slot.StartTimeMinutes)] = false
	}

	return &userSlot, &userSlotInfo, nil
}

// initialize,
// 1. creates required tables in the sqlite table
// 2. hydrates constants
func initialize() {
	db, err := sql.Open("sqlite3", file)
	if err != nil {
		panic(err)
		// TODO: panic
	}
	s := server{
		db: db,
	}
	if _, err := s.db.Exec(userCreate); err != nil {
		panic(err)
	}

	if _, err := s.db.Exec(availabilityCreate); err != nil {
		panic(err)
	}

	if _, err := s.db.Exec(bookedSlots); err != nil {
		panic(err)
	}
	dayOfTheWeekMap[time.Monday] = "monday"
	dayOfTheWeekMap[time.Tuesday] = "tuesday"
	dayOfTheWeekMap[time.Wednesday] = "wednesday"
	dayOfTheWeekMap[time.Thursday] = "thursday"
	dayOfTheWeekMap[time.Friday] = "friday"
	dayOfTheWeekMap[time.Saturday] = "saturday"
	dayOfTheWeekMap[time.Sunday] = "sunday"
}

// main()
func main() {
	r := gin.Default()
	initialize()
	v1 := r.Group("/v1")
	{
		v1.POST("/create-user", createUser)
		v1.POST("/user/view-schedule", viewSchedule)
		v1.POST("/user/set-availability", setAvailability)
		v1.POST("/user/find-available-slots", findAvailableSlots)
		v1.POST("/user/book-slot", bookSlot)
	}
	err := r.Run()
	if err != nil {
		panic("unable to run")
	}
}
