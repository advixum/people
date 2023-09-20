package models

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"people/logging"
	"regexp"
	"strings"
	"sync"

	"gorm.io/gorm"
)

var log = logging.Config

// The model for parsing data from the Apache Kafka messages.
type FullName struct {
	Name       string
	Surname    string
	Patronymic string
	Error      string
}

// The method of the data validity checking in the FullName model.
func (e *FullName) IsValid() string {
	namePattern := `^[a-zA-Zа-яА-Я]+$`
	var errContent []string
	// Name
	switch {
	case e.Name == "":
		errContent = append(errContent, "name cannot be empty")
	case len(e.Name) < 2:
		errContent = append(errContent, "name is too short")
	case len(e.Name) > 50:
		errContent = append(errContent, "name is too long")
	case !regexp.MustCompile(namePattern).MatchString(e.Name):
		errContent = append(errContent, "name contains invalid characters")
	}
	// Surname
	switch {
	case e.Surname == "":
		errContent = append(errContent, "surname cannot be empty")
	case len(e.Surname) < 2:
		errContent = append(errContent, "surname is too short")
	case len(e.Surname) > 50:
		errContent = append(errContent, "surname is too long")
	case !regexp.MustCompile(namePattern).MatchString(e.Surname):
		errContent = append(errContent, "surname contains invalid characters")
	}
	if len(errContent) == 0 {
		return ""
	}
	err := strings.Join(errContent, ", ")
	return err
}

// The model for parsing data into GraphQL answers.
type GraphQL struct {
	ID          uint
	Name        string
	Surname     string
	Patronymic  string
	Age         uint8
	Gender      string
	Nationality string
}

// The model for saving data in the database.
type Entry struct {
	gorm.Model
	ID          uint   `gorm:"primarykey"`
	Name        string `gorm:"not null"`
	Surname     string `gorm:"not null"`
	Patronymic  string `gorm:"default:''"`
	Age         uint8  `gorm:"not null"`
	Gender      string `gorm:"not null"`
	Nationality string `gorm:"not null"`
}

// The method of the data validity checking in the Entry model.
func (e *Entry) IsValid() error {
	namePattern := `^[a-zA-Zа-яА-Я]+$`
	countryPattern := `^[A-Z]{2}$`
	var errContent []string
	// Name
	switch {
	case e.Name == "":
		errContent = append(errContent, "name cannot be empty")
	case len(e.Name) < 2:
		errContent = append(errContent, "name is too short")
	case len(e.Name) > 50:
		errContent = append(errContent, "name is too long")
	case !regexp.MustCompile(namePattern).MatchString(e.Name):
		errContent = append(errContent, "name contains invalid characters")
	}
	// Surname
	switch {
	case e.Surname == "":
		errContent = append(errContent, "surname cannot be empty")
	case len(e.Surname) < 2:
		errContent = append(errContent, "surname is too short")
	case len(e.Surname) > 50:
		errContent = append(errContent, "surname is too long")
	case !regexp.MustCompile(namePattern).MatchString(e.Surname):
		errContent = append(errContent, "surname contains invalid characters")
	}
	// Age
	if e.Age < 1 || e.Age > 120 {
		errContent = append(errContent, "age contains invalid data")
	}
	// Gender
	switch {
	case e.Gender == "":
		errContent = append(errContent, "gender cannot be empty")
	case e.Gender != "male" && e.Gender != "female":
		errContent = append(
			errContent, `only “male” or “female” gender is available`,
		)
	}
	// Nationality
	switch {
	case e.Nationality == "":
		errContent = append(errContent, "nationality cannot be empty")
	case !regexp.MustCompile(countryPattern).MatchString(e.Nationality):
		errContent = append(
			errContent, `nationality contains invalid data (example: RU, US)`,
		)
	}
	if len(errContent) == 0 {
		return nil
	}
	err := strings.Join(errContent, ", ")
	return errors.New(err)
}

// The method for enrich Apache Kafka messages by age, gender and
// nationality. It fills the model Entry from API, otherwise return an
// error.
func (e *Entry) Enrich(name string) error {
	f := logging.F()
	errCh := make(chan error, 1)
	var tasks sync.WaitGroup
	tasks.Add(3)
	go age(name, &e.Age, &tasks, errCh)
	go gender(name, &e.Gender, &tasks, errCh)
	go nationality(name, &e.Nationality, &tasks, errCh)
	go func() {
		tasks.Wait()
		close(errCh)
	}()
	for err := range errCh {
		log.Error(f+"failed to enrich data from API: ", err)
		return err
	}
	return nil
}

// Gorutin for obtaining age data based on a name.
func age(name string, age *uint8, wg *sync.WaitGroup, ch chan error) {
	defer wg.Done()
	url := fmt.Sprintf("https://api.agify.io/?name=%s", name)
	var reqData map[string]interface{}
	err := apiReq(url, &reqData)
	if err != nil {
		ch <- err
	}
	target, ok := reqData["age"].(float64) // int float64
	if !ok {
		ch <- errors.New("age data not found")
	}
	*age = uint8(target)
}

// Gorutin for obtaining gender data based on a name.
func gender(name string, gender *string, wg *sync.WaitGroup, ch chan error) {
	defer wg.Done()
	url := fmt.Sprintf("https://api.genderize.io/?name=%s", name)
	var reqData map[string]interface{}
	err := apiReq(url, &reqData)
	if err != nil {
		ch <- err
	}
	target, ok := reqData["gender"].(string)
	if !ok {
		ch <- errors.New("gender data not found")
	}
	//time.Sleep(3 * time.Second)
	*gender = target
}

// Gorutin for obtaining nationality data based on a name.
func nationality(
	name string, nation *string, wg *sync.WaitGroup, ch chan error,
) {
	defer wg.Done()
	url := fmt.Sprintf("https://api.nationalize.io/?name=%s", name)
	var reqData map[string]interface{}
	err := apiReq(url, &reqData)
	if err != nil {
		ch <- err
	}
	countryList, ok := reqData["country"].([]interface{})
	if !ok || len(countryList) == 0 {
		ch <- errors.New("country data not found")
	}
	firstCountry, ok := countryList[0].(map[string]interface{})
	if !ok {
		ch <- errors.New("invalid country data")
	}
	countryID, ok := firstCountry["country_id"].(string)
	if !ok {
		ch <- errors.New("country ID not found")
	}
	//time.Sleep(3 * time.Second)
	*nation = countryID
}

// The function of processing the request to the specified url. Fills
// out data map from the response body, otherwise returns an error.
func apiReq(url string, reqData *map[string]interface{}) error {
	response, err := http.Get(url)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	err = json.NewDecoder(response.Body).Decode(&reqData)
	if err != nil {
		return err
	}
	return nil
}
