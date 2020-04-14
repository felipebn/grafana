package middleware

import (
	"fmt"
	"github.com/dgrijalva/jwt-go"
	"gopkg.in/macaron.v1"
	"gopkg.in/square/go-jose.v2"
	"io/ioutil"
	"net/http"
	"testing"

	"github.com/grafana/grafana/pkg/bus"
	m "github.com/grafana/grafana/pkg/models"
	"github.com/grafana/grafana/pkg/setting"
	. "github.com/smartystreets/goconvey/convey"

	"encoding/json"
	"github.com/grafana/grafana/pkg/infra/log"
	"os"
	"path/filepath"
)

// Capture output from an internal render
type CaptureRender struct {
	macaron.DummyRender

	status int
	body   interface{}
}

func (r *CaptureRender) JSON(s int, b interface{}) {
	r.status = s
	r.body = b
}

func TestAuthJWT(t *testing.T) {
	pwd, err := os.Getwd()
	if err != nil {
		t.Fatal("Unable to get working directory", err)
	}
	pwd = filepath.Clean(pwd + "/../util")

	Convey("When using JWT auth", t, func() {

		orgId := int64(1)
		bus.ClearBusHandlers()
		bus.AddHandler("test", func(query *m.GetSignedInUserQuery) error {
			query.Result = &m.SignedInUser{
				OrgId:  query.OrgId,
				UserId: 123,
				Email:  query.Email,
			}
			return nil
		})

		eck1Bytes, errorLoadingEk1 := ioutil.ReadFile(filepath.Clean(pwd + "/jwt_ec_key_1_priv.json"))
		So(errorLoadingEk1, ShouldBeNil)

		eck1 := &jose.JSONWebKey{}
		err := json.Unmarshal(eck1Bytes, eck1)
		So(err, ShouldBeNil)

		// chain of two public EC keys
		pathToGoogleJwk := filepath.Clean(pwd + "/jwt_ec_jwk.json")
		setting.AuthJwtEnabled = true
		setting.AuthJwtHeader = "X-MyJWT"
		setting.AuthJwtVerification = pathToGoogleJwk
		setting.AuthJwtEmailClaim = "email"
		InitAuthJwtKey()

		// Create the Claims
		claims := &jwt.MapClaims{
			"sub":   "name",
			"email": "test@grafana.com",
		}

		rawToken := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
		rawToken.Header["kid"] = "theveryfirstkey"
		signed, err := rawToken.SignedString(eck1.Key)
		So(err, ShouldEqual, nil)

		Convey("Should be able to decode JWT directly", func() {
			parsedToken, err := jwt.Parse(signed, func(token *jwt.Token) (interface{}, error) {
				return eck1.Public().Key, nil
			})

			So(err, ShouldEqual, nil)
			So(parsedToken.Valid, ShouldEqual, true)

			parsedClaims := parsedToken.Claims.(jwt.MapClaims)
			So(parsedClaims["email"], ShouldEqual, "test@grafana.com")
			So(parsedClaims["sub"], ShouldEqual, "name")
		})

		Convey("Context should read it from header and find a user", func() {
			httpreq := &http.Request{Header: make(http.Header)}
			httpreq.Header.Add(setting.AuthJwtHeader, signed)
			render := &CaptureRender{}

			ctx := &m.ReqContext{Context: &macaron.Context{
				Req:    macaron.Request{Request: httpreq},
				Render: render,
			},
				Logger: log.New("fakelogger"),
			}

			initContextWithJwtAuth(ctx, orgId)
			So(ctx.SignedInUser, ShouldNotBeNil)
			So(ctx.SignedInUser.Email, ShouldEqual, "test@grafana.com")
		})

		Convey("Context should throw an error with invalid JWTs", func() {
			httpreq := &http.Request{Header: make(http.Header)}
			httpreq.Header.Add(setting.AuthJwtHeader, "NOT-A-JWT")
			render := &CaptureRender{}
			ctx := &m.ReqContext{Context: &macaron.Context{
				Req:    macaron.Request{Request: httpreq},
				Render: render,
			},
				Logger: log.New("fakelogger"),
			}

			initContextWithJwtAuth(ctx, orgId)
			So(ctx.SignedInUser, ShouldBeNil)
			So(render.status, ShouldEqual, 400)
		})

		Convey("A jwt not signed by the id indicated by 'kid'", func() {
			rawToken := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
			rawToken.Header["kid"] = "thesecondkey"
			signed, err := rawToken.SignedString(eck1.Key)

			So(err, ShouldBeNil)
			So(signed, ShouldNotBeNil)

			Convey("When an http request is made using the key with non-expected signer", func() {
				httpreq := &http.Request{Header: make(http.Header)}
				httpreq.Header.Add(setting.AuthJwtHeader, signed)
				render := &CaptureRender{}
				ctx := &m.ReqContext{Context: &macaron.Context{
					Req:    macaron.Request{Request: httpreq},
					Render: render,
				},
					Logger: log.New("fakelogger"),
				}

				initContextWithJwtAuth(ctx, orgId)
				So(ctx.SignedInUser, ShouldBeNil)
				So(render.status, ShouldEqual, 401)
			})
		})

		Convey("A context having expected claims settings", func() {
			setting.AuthJwtExpectClaims = make(map[string]string)
			setting.AuthJwtExpectClaims["aud"] = "quietaudience"
			InitAuthJwtKey()

			So(decoder.CheckReady(), ShouldBeTrue)

			Convey("A request is made with a JWT having unexpected claims", func() {
				newClaims := make(jwt.MapClaims)
				for k, v := range *claims {
					newClaims[k] = v
				}
				newClaims["aud"] = "rowdyaudience"

				rawToken := jwt.NewWithClaims(jwt.SigningMethodES256, newClaims)
				rawToken.Header["kid"] = "theveryfirstkey"
				signed, _ := rawToken.SignedString(eck1.Key)

				httpreq := &http.Request{Header: make(http.Header)}
				httpreq.Header.Add(setting.AuthJwtHeader, signed)
				render := &CaptureRender{}
				ctx := &m.ReqContext{Context: &macaron.Context{
					Req:    macaron.Request{Request: httpreq},
					Render: render,
				},
					Logger: log.New("fakelogger"),
				}

				initContextWithJwtAuth(ctx, orgId)
				So(ctx.SignedInUser, ShouldBeNil)
				So(render.status, ShouldEqual, 401)
			})
		})

		Convey("Should fail to parse invalid key sets", func() {
			setting.AuthJwtVerification = "NOT A KEY"
			InitAuthJwtKey()
			So(decoder.CheckReady(), ShouldBeFalse)
		})

		//Check Firebase Support
		Convey("Should parse firebase tokens", func() {
			setting.AuthJwtLoginClaim = "email"
			setting.AuthJwtVerification = pwd + "/jwt_test_data.firebase.json" //https://www.googleapis.com/robot/v1/metadata/x509/securetoken@system.gserviceaccount.com"
			setting.AuthJwtExpectClaims = make(map[string]string)
			setting.AuthJwtExpectClaims["iss"] = "https://securetoken.google.com/safetronx"
			InitAuthJwtKey()

			So(decoder.CheckReady(), ShouldBeTrue)

			// Expired token
			fbjwt := "eyJhbGciOiJSUzI1NiIsImtpZCI6Ijg1OWE2NDFhMWI4MmNjM2I1MGE4MDFiZjUwNjQwZjM4MjU3ZDEyOTkiLCJ0eXAiOiJKV1QifQ.eyJpc3MiOiJodHRwczovL3NlY3VyZXRva2VuLmdvb2dsZS5jb20vc2FmZXRyb254IiwibmFtZSI6IlJ5YW4gTWNLaW5sZXkiLCJwaWN0dXJlIjoiaHR0cHM6Ly9saDUuZ29vZ2xldXNlcmNvbnRlbnQuY29tLy12M0diUy1namhlcy9BQUFBQUFBQUFBSS9BQUFBQUFBQUNIZy94ZE5VbDRmMUdEZy9waG90by5qcGciLCJhdWQiOiJzYWZldHJvbngiLCJhdXRoX3RpbWUiOjE1NDkwNDIzNzUsInVzZXJfaWQiOiJyalNaZm9LYnZYU1pyRGg3SUVmOGRid0Mxa2kxIiwic3ViIjoicmpTWmZvS2J2WFNackRoN0lFZjhkYndDMWtpMSIsImlhdCI6MTU0OTA0MjM3NSwiZXhwIjoxNTQ5MDQ1OTc1LCJlbWFpbCI6InJ5YW50eHVAZ21haWwuY29tIiwiZW1haWxfdmVyaWZpZWQiOnRydWUsImZpcmViYXNlIjp7ImlkZW50aXRpZXMiOnsiZ29vZ2xlLmNvbSI6WyIxMDM3Nzg4NDE3Nzk5OTQ4ODI1MTIiXSwiZW1haWwiOlsicnlhbnR4dUBnbWFpbC5jb20iXX0sInNpZ25faW5fcHJvdmlkZXIiOiJnb29nbGUuY29tIn19.YPgqDMZAXUQPPR3ofDBl4vIK1amQQLsmo9OQvM0v9f98hDWcwVIPBh34CWFum40DA-H6JDqiGMbqcPl8LPUewRU01GdbR1QV7FvL_n2UQOLSJWcRnyi-LBK2TtkQ6fRpNNrX-E3lwgNq_GnegkEW1NZnPqpLZsN67kflGh5c7tC45v0osvFT-X8LjWxww4PijoZZsTdF2GRkuRYGLWQ1v99dhr9y8QhXHtTiHS6D9bjZ53K7t8CBKiZ5Ibkr4wZhz5-mW-6PibzTX-u2JeIzQFZo9tQM7-T526oVU19d7O-P5PU_kNmHe99PyDt2drtBbUPNn9IeenvIrz6rOKau6g"

			// This should give an exception since it is expired
			httpreq := &http.Request{Header: make(http.Header)}
			httpreq.Header.Add(setting.AuthJwtHeader, fbjwt)
			render := &CaptureRender{}
			ctx := &m.ReqContext{Context: &macaron.Context{
				Req:    macaron.Request{Request: httpreq},
				Render: render,
			},
				Logger: log.New("fakelogger"),
			}
			initContextWithJwtAuth(ctx, orgId)
			So(ctx.SignedInUser, ShouldBeNil)
			So(render.status, ShouldEqual, 401)
		})

		// Check Google JWK/IAP Support
		Convey("Should parse JWK tokens", func() {
			setting.AuthJwtVerification = "https://www.gstatic.com/iap/verify/public_key-jwk"
			setting.AuthJwtExpectClaims = nil
			InitAuthJwtKey()

			fmt.Printf("AFTER %v\n", decoder)

			So(decoder.CheckReady(), ShouldBeTrue)
		})
	})
}
