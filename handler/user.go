package handle

import (
	"log"
	"math/rand"
	"net/http"
	"regexp"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/keller0/yxi-back/model"
)

type login struct {
	User     string `form:"user" json:"user" binding:"required"`
	Password string `form:"password" json:"password" binding:"required"`
}

type register struct {
	User     string `form:"user" json:"user" binding:"required"`
	Password string `form:"password" json:"password" binding:"required"`
	Email    string `form:"email" json:"email" binding:"required"`
}

func init() {
	rand.Seed(time.Now().UnixNano())
}

// Login return a jwt if user info is valid.
// 200 400 401 404
func Login(c *gin.Context) {
	var err error
	var loginJSON login
	if err = c.ShouldBindJSON(&loginJSON); err == nil {
		var user model.User
		user.Username = loginJSON.User
		user.Password = loginJSON.Password

		if !user.UsernameExist() {
			// return if username already exists
			c.JSON(http.StatusNotFound, gin.H{"errNumber": responseErr["UserNotExist"]})
			c.Abort()
			return
		}
		tokenString, err := user.Login()
		if err != nil {
			log.Println(err.Error())
			c.JSON(http.StatusUnauthorized, gin.H{"errNumber": responseErr["Wrong Password"]})
		} else {
			c.JSON(http.StatusOK, gin.H{"token": tokenString})
		}
	} else {
		c.JSON(http.StatusBadRequest, gin.H{"errNumber": responseErr["Bad Requset"]})
	}

}

// CheckUserExist use http post check if username already exists
func CheckUserExist(c *gin.Context) {
	var username string
	if err := c.ShouldBindQuery(username); err != nil {
		var user model.User
		user.Username = username
		if user.UsernameExist() {
			c.JSON(http.StatusBadRequest, gin.H{"errNumber": responseErr["User Already Exist"]})
		} else {
			c.JSON(http.StatusOK, gin.H{"error": ""})
		}
	} else {
		c.JSON(http.StatusBadRequest, gin.H{"errNumber": responseErr["Bad Requset"]})
	}
}

// Register use post data to create a user account
// 200 400 409 500
func Register(c *gin.Context) {
	var err error
	var registJSON register

	err = c.ShouldBindJSON(&registJSON)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"errNumber": responseErr["Bad Requset"]})
		c.Abort()
		return
	}

	if es := registJSON.Validate(); es != "" {
		c.JSON(http.StatusBadRequest, gin.H{"errNumber": es})
		c.Abort()
		return
	}
	var user model.User
	user.Username = registJSON.User
	user.Email = registJSON.Email

	if user.UsernameExist() {
		// return if username already exists
		c.JSON(http.StatusConflict, gin.H{"errNumber": responseErr["User Already Exist"]})
		c.Abort()
		return
	}
	if user.EmailExist() {
		// return if username already exists
		c.JSON(http.StatusConflict, gin.H{"errNumber": responseErr["Email Already Exist"]})
		c.Abort()
		return
	}

	user.Password = registJSON.Password
	e := user.New()
	if e != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"errNumber": responseErr["ServerErr Register Failed"]})
	}
	c.String(http.StatusOK, "registration succeeded")
}

func (r *register) Validate() string {
	re := regexp.MustCompile("^[a-zA-Z0-9.!#$%&'*+/=?^_`{|}~-]+@[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?(?:\\.[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)*$")
	reName := regexp.MustCompile("^[a-zA-Z0-9]+$")
	switch {
	case !re.MatchString(r.Email):
		return responseErr["Email is not valid"]
	case !reName.MatchString(r.User):
		return responseErr["Username is not valid"]
	case len(r.User) > 15:
		return responseErr["Username is too long"]
	case len(r.Password) < 9:
		return responseErr["Password is too short"]
	}
	return ""
}
