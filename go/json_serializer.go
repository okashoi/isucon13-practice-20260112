package main

import (
	json "encoding/json/v2"
	"github.com/labstack/echo/v4"
)

type V2JSONSerializer struct{}

func (s *V2JSONSerializer) Serialize(c echo.Context, i interface{}, indent string) error {
	return json.MarshalWrite(c.Response(), i)
}

func (s *V2JSONSerializer) Deserialize(c echo.Context, i interface{}) error {
	return json.UnmarshalRead(c.Request().Body, i)
}
