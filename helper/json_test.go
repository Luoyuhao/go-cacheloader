package helper

type TestStruct struct {
	Name  string `json:"name"`
	Value int64  `json:"value"`
}

func (suite *Suite) TestJSONMarshal() {
	var data interface{}
	data = &TestStruct{Name: "test", Value: 1}
	data, err := JSONMarshal(data)
	suite.NoError(err)
	// newData := &TestStruct{}
	// err = JSONUnmarshal(str, newData)
	// suite.NoError(err)
	// suite.Equal(data, newData)
}
