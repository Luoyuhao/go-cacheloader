package internal

import (
	"testing"

	"github.com/stretchr/testify/suite"
)

// Define the suite, and absorb the built-in basic suite
// functionality from testify - including a T() method which
// returns the current testing context
type Suite struct {
	suite.Suite
}

// Make sure that VariableThatShouldStartAtFive is set to five
// before each test
func (suite *Suite) SetupSuite() {
}

// The SetupTest method will be run before every test in the suite.
func (suite *Suite) SetupTest() {
}

// The TearDownTest method will be run after every test in the suite.
func (suite *Suite) TearDownTest() {
}

func (suite *Suite) TearDownSuite() {
}

// In order for 'go test' to run this suite, we need to create
// a normal test function and pass our suite to suite.Run
func TestSuite(t *testing.T) {
	suite.Run(t, new(Suite))
}
