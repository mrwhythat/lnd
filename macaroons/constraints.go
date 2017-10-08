package macaroons

import (
	"errors"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/macaroon-bakery.v1/bakery/checkers"
	macaroon "gopkg.in/macaroon.v1"
)

// Constraint type adds a layer of indirection over macaroon caveats and
// checkers.
type Constraint func(*macaroon.Macaroon) error

// AddConstraints returns new derived macaroon by applying every passed
// constraint and tightening its restrictions.
func AddConstraints(mac *macaroon.Macaroon, cs ...Constraint) (*macaroon.Macaroon, error) {
	newMac := mac.Clone()
	for _, constraint := range cs {
		if err := constraint(newMac); err != nil {
			return nil, err
		}
	}
	return newMac, nil
}

// Each *Constraint function is a functional option, which takes a pointer
// to the macaroon and adds another restriction to it. For each *Constraint,
// the corresponding *Checker is provided.

// AllowConstraint restricts allowed operations set to the ones
// passed to it.
func AllowConstraint(ops ...string) func(*macaroon.Macaroon) error {
	return func(mac *macaroon.Macaroon) error {
		caveat := checkers.AllowCaveat(ops...)
		return mac.AddFirstPartyCaveat(caveat.Condition)
	}
}

// AllowChecker wraps default checkers.OperationChecker.
func AllowChecker(method string) checkers.Checker {
	return checkers.OperationChecker(method)
}

// TimeoutConstraint restricts the lifetime of the macaroon
// to the amount of seconds given.
func TimeoutConstraint(seconds int64) func(*macaroon.Macaroon) error {
	return func(mac *macaroon.Macaroon) error {
		macaroonTimeout := time.Duration(seconds)
		requestTimeout := time.Now().Add(time.Second * macaroonTimeout)
		caveat := checkers.TimeBeforeCaveat(requestTimeout)
		return mac.AddFirstPartyCaveat(caveat.Condition)
	}
}

// TimeoutChecker wraps default checkers.TimeBefore checker.
func TimeoutChecker() checkers.Checker {
	return checkers.TimeBefore
}

// IPLockConstraint locks macaroon to a specific IP address.
// If address is an empty string, this constraint does nothing to
// accommodate default value's desired behavior.
func IPLockConstraint(ipAddr string) func(*macaroon.Macaroon) error {
	return func(mac *macaroon.Macaroon) error {
		if ipAddr != "" {
			macaroonIPAddr := net.ParseIP(ipAddr)
			if macaroonIPAddr == nil {
				return fmt.Errorf("incorrect macaroon IP-lock address")
			}
			caveat := checkers.ClientIPAddrCaveat(macaroonIPAddr)
			return mac.AddFirstPartyCaveat(caveat.Condition)
		}
		return nil
	}
}

// IPLockChecker accepts client IP from the validation context and compares it
// with IP locked in the macaroon.
func IPLockChecker(clientIP string) checkers.Checker {
	return checkers.CheckerFunc{
		Condition_: checkers.CondClientIPAddr,
		Check_: func(_, cav string) error {
			if !net.ParseIP(cav).Equal(net.ParseIP(clientIP)) {
				msg := "macaroon locked to different IP address"
				return fmt.Errorf(msg)
			}
			return nil
		},
	}
}

// PaymentPathConstraint limits some parts of the payment path to certain nodes
func PaymentPathConstraint(pred string) func(*macaroon.Macaroon) error {
	return func(macaroon *macaroon.Macaroon) error {
		if pred != "" {
			if _, err := parseRouteConstraint(pred); err != nil {
				return err
			}
			caveat := routeCaveat(pred)
			return macaroon.AddFirstPartyCaveat(caveat.Condition)
		}
		return nil
	}
}

// routeConstraintID is a caveat identifier for the payment path constraint.
const routeConstraintID = "payment-path-constraint"

type routeConstraint struct {
	index   int
	negate  bool
	nodeSet []string
}

// parseRouteConstraint checks the validity of the route constraint and
// returns struct representing this constraint.
//
// Example of route constraint strings:
//          "path[index]     in {node1, node2, node3}"
//          "path[index] not in {node1, node2}"
func parseRouteConstraint(predicate string) (*routeConstraint, error) {
	constraintRegex := `path\[(-?[0-9]+)\]\s+(not)?\s*in\s+{(.*)}`
	constraintMatcher, err := regexp.Compile(constraintRegex)
	if err != nil {
		return nil, err
	}
	match := constraintMatcher.FindStringSubmatch(predicate)
	if match == nil {
		return nil, errors.New("Path constraint syntax error")
	}
	ind, err := strconv.Atoi(match[1])
	if err != nil {
		return nil, errors.New("Unable to parse path index")
	}
	var neg bool
	if match[2] == "not" {
		neg = true
	} else if match[2] != "" {
		return nil, errors.New("Incorrect path constraint negation")
	}
	nodes := make([]string, 0)
	for _, node := range strings.Split(match[3], ",") {
		nodes = append(nodes, strings.Trim(node, " "))
	}
	return &routeConstraint{index: ind, negate: neg, nodeSet: nodes}, nil
}

func routeCaveat(predicate string) checkers.Caveat {
	return checkers.Caveat{
		Condition: routeConstraintID + " " + predicate,
	}
}

// PaymentPathChecker receives slice of base58-encoded node pubkeys and
// checks if it satisfies the payment path constraint in the macaroon.
func PaymentPathChecker(path []string) checkers.Checker {
	checkerFunc := func(_, cav string) error {
		// Check if constraint is valid.
		routeConstraint, err := parseRouteConstraint(cav)
		if err != nil {
			return err
		}

		// Sanitize index value.
		index := routeConstraint.index
		if index >= len(path) || index < -len(path) {
			msg := "path constraint index exceeds path length"
			return errors.New(msg)
		}
		if index < 0 {
			index += len(path)
		}

		// Check whether path element is [not] in constraint node set.
		var ok bool
		nodeID := path[index]
		for _, constraintNode := range routeConstraint.nodeSet {
			if nodeID == constraintNode {
				ok = true
				break
			}
		}
		if routeConstraint.negate {
			ok = !ok
		}
		if !ok {
			msg := "path does not satisfy constraint \"%s\""
			return fmt.Errorf(msg, cav)
		}

		return nil
	}
	return checkers.CheckerFunc{
		Condition_: routeConstraintID,
		Check_:     checkerFunc,
	}
}
