// Copyright (c) 2017-2018 Townsourced Inc.

package sequence

import (
	"errors"
	"fmt"
	"io/ioutil"
	"net/url"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/tebeka/selenium"
)

// Sequence is a helper structs of chaining selecting elements and testing them
// if any part of the sequence fails the sequence ends and returns the error
// built to make writing tests easier
type Sequence struct {
	driver          selenium.WebDriver
	err             *Error
	EventualPoll    time.Duration
	EventualTimeout time.Duration
	last            func() *Sequence
	onErr           func(Error, *Sequence)
}

// Error describes an error that occured during the sequence processing.
type Error struct {
	Stage   string
	Element selenium.WebElement
	Err     error
	Caller  string
}

// caller returns the caller (file and line number) of the function from the perspective of where this caller function
// is called.  I.E caller(0) returns the immediate function call before the function calling caller(0)
// If you wrap caller in a function, add one, etc
func caller(skip int) string {
	skip += 2 // increment for this function call and the calling function

	// get the file and line where the function was called from
	_, file, line, ok := runtime.Caller(skip)
	if !ok {
		return ""
	}

	file = filepath.Base(file)

	return fmt.Sprintf("%s:%d", file, line)
}

// Error fulfills the error interface
func (e *Error) Error() string {
	if e.Element != nil {
		return fmt.Sprintf("An error occurred at %s during %s on element %s: %s", e.Caller, e.Stage,
			elementString(e.Element), e.Err)
	}
	return fmt.Sprintf("An error occurred at %s during %s:  %s", e.Caller, e.Stage, e.Err)
}

// Errors is multiple sequence errors
type Errors []error

func (e Errors) Error() string {
	str := "Multiple errors occurred: \n"
	for i := range e {
		str += "\t" + e[i].Error() + "\n"
	}
	return str
}

func elementString(element selenium.WebElement) string {
	if element == nil {
		return ""
	}
	id, err := element.GetAttribute("id")
	if err == nil && id != "" {
		return fmt.Sprintf("#%s", id)
	}
	tag, err := element.TagName()
	if err != nil {
		return fmt.Sprintf("%v", element)
	}
	text, err := element.Text()
	if err != nil {
		return fmt.Sprintf("%v", element)
	}

	if len(text) > 25 {
		text = text[:25]
	}

	return fmt.Sprintf("<%s>%s</%s>", tag, text, tag)
}

// Elements is a collections of web elements
type Elements struct {
	seq        *Sequence
	elems      []selenium.WebElement
	selector   string
	selectFunc func(selector string) ([]selenium.WebElement, error)
	last       func() *Elements
	all        bool
	any        bool
}

// Start starts a new sequence of tests
func Start(driver selenium.WebDriver) *Sequence {
	return &Sequence{
		driver:          driver,
		EventualPoll:    100 * time.Millisecond,
		EventualTimeout: 60 * time.Second,
	}
}

// End ends a sequence and returns any errors
func (s *Sequence) End() error {
	if s.err != nil {
		if s.onErr != nil {
			s.onErr(*s.err, s)
		}
		return s.err
	}
	return nil
}

// OK ends a sequence and fails and stopped the tests passed in if the sequence is in error
func (s *Sequence) Ok(tb testing.TB) {
	if s.err != nil {
		if s.onErr != nil {
			s.onErr(*s.err, s)
		}

		fmt.Printf("Sequence failed: %s", s.err)
		tb.FailNow()
	}
}

// OnError registers a function to call when an error occurs in the sequence.
// Handy for calling things like .Debug() and .Screenshot("err.png") in error scenarios to output to
// a CI server
// OnError must be called before any errors in order for it to be triggered properly
func (s *Sequence) OnError(fn func(err Error, s *Sequence)) *Sequence {
	s.onErr = fn
	return s
}

// Driver returns the underlying WebDriver
func (s *Sequence) Driver() selenium.WebDriver {
	return s.driver
}

// Eventually will retry the previous test if it returns an error every EventuallyPoll duration until EventualTimeout
// is reached
func (s *Sequence) Eventually() *Sequence {
	if s.err == nil {
		return s
	}

	err := s.driver.WaitWithTimeoutAndInterval(func(d selenium.WebDriver) (bool, error) {
		s.err = nil
		s = s.last()
		if s.err != nil {
			return false, nil
		}
		return true, nil
	}, s.EventualTimeout, s.EventualPoll)
	if err != nil {
		s.err.Caller = caller(0)
	}
	return s
}

// Eventually will retry the previous test if it returns an error every EventuallyPoll duration until EventualTimeout
// is reached
func (e *Elements) Eventually() *Elements {
	if e.seq.err == nil {
		return e
	}

	if e.selectFunc == nil || e.selector == "" {
		return e
	}

	err := e.seq.driver.WaitWithTimeoutAndInterval(func(d selenium.WebDriver) (bool, error) {
		e.seq.err = nil
		var err error
		e.elems, err = e.selectFunc(e.selector)
		if err != nil {
			e.seq.err = &Error{
				Stage:  "Elements",
				Err:    err,
				Caller: caller(1),
			}
			return false, nil
		}
		e = e.last()
		if e.seq.err != nil {
			return false, nil
		}
		return true, nil
	}, e.seq.EventualTimeout, e.seq.EventualPoll)
	if err != nil {
		e.seq.err.Caller = caller(0)
	}
	return e
}

// Test runs an arbitrary test against the entire page
func (s *Sequence) Test(testName string, fn func(d selenium.WebDriver) error) *Sequence {
	if s.err != nil {
		return s
	}
	s = s.test(testName, fn)
	if s.err != nil {
		s.err.Caller = caller(0)
	}
	return s
}

func (s *Sequence) test(testName string, fn func(d selenium.WebDriver) error) *Sequence {
	s.last = func() *Sequence {
		if s.err != nil {
			return s
		}

		err := fn(s.driver)

		if err != nil {
			s.err = &Error{
				Stage:  testName,
				Err:    err,
				Caller: caller(2),
			}
		}
		return s
	}
	return s.last()
}

// TitleMatch is for testing the value of the title
type TitleMatch struct {
	title string
	s     *Sequence
}

func (t *TitleMatch) test(testName string, fn func() error) *Sequence {
	t.s.last = func() *Sequence {
		if t.s.err != nil {
			return t.s
		}
		title, err := t.s.driver.Title()
		if err != nil {
			t.s.err = &Error{
				Stage:  "Title " + testName,
				Err:    err,
				Caller: caller(2),
			}
			return t.s
		}
		t.title = title
		err = fn()
		if err != nil {
			t.s.err = &Error{
				Stage:  "Title " + testName,
				Err:    err,
				Caller: caller(2),
			}
		}
		return t.s
	}
	return t.s.last()
}

// Equals tests if the title matches the passed in value exactly
func (t *TitleMatch) Equals(match string) *Sequence {
	return t.test("Equals", func() error {
		if t.title != match {
			return fmt.Errorf("The page's title does not equal '%s'. Got '%s'", match, t.title)
		}
		return nil
	})
}

// Contains tests if the title contains the passed in value
func (t *TitleMatch) Contains(match string) *Sequence {
	return t.test("Contains", func() error {
		if !strings.Contains(t.title, match) {
			return fmt.Errorf("The pages's title does not contain '%s'. Got '%s'", match, t.title)
		}
		return nil
	})
}

// StartsWith tests if the title starts with the passed in value
func (t *TitleMatch) StartsWith(match string) *Sequence {
	return t.test("Starts With", func() error {
		if !strings.HasPrefix(t.title, match) {
			return fmt.Errorf("The pages's title does not start with '%s'. Got '%s'", match, t.title)
		}
		return nil
	})
}

// EndsWith tests if the title ends with the passed in value
func (t *TitleMatch) EndsWith(match string) *Sequence {
	return t.test("Ends With", func() error {
		if !strings.HasSuffix(t.title, match) {
			return fmt.Errorf("The pages's title does not end with '%s'. Got '%s'", match, t.title)
		}
		return nil
	})
}

// Regexp tests if the title matches the regular expression
func (t *TitleMatch) Regexp(exp *regexp.Regexp) *Sequence {
	return t.test("Matches RegExp", func() error {
		if !exp.MatchString(t.title) {
			return fmt.Errorf("The pages's title does not match the regular expression '%s'. Title: '%s'",
				exp, t.title)
		}
		return nil
	})
}

// Title checks the match against the page's title
func (s *Sequence) Title() *TitleMatch {
	return &TitleMatch{
		s: s,
	}
}

// Get navigates to the passed in URI
func (s *Sequence) Get(uri string) *Sequence {
	s.last = func() *Sequence {
		if s.err != nil {
			return s
		}
		err := s.driver.Get(uri)
		if err != nil {
			s.err = &Error{
				Stage:  "Get",
				Err:    err,
				Caller: caller(1),
			}
		}
		return s
	}
	return s.last()
}

// URLMatch is for testing the value of the page's URL
type URLMatch struct {
	url *url.URL
	s   *Sequence
}

func (u *URLMatch) test(testName string, fn func() error) *Sequence {
	u.s.last = func() *Sequence {
		if u.s.err != nil {
			return u.s
		}
		uri, err := u.s.driver.CurrentURL()
		if err != nil {
			u.s.err = &Error{
				Stage:  "URL " + testName,
				Err:    err,
				Caller: caller(2),
			}
			return u.s
		}

		u.url, err = url.Parse(uri)
		if err != nil {
			u.s.err = &Error{
				Stage:  "URL " + testName,
				Err:    err,
				Caller: caller(2),
			}
			return u.s
		}
		err = fn()
		if err != nil {
			u.s.err = &Error{
				Stage:  "URL " + testName,
				Err:    err,
				Caller: caller(2),
			}
		}
		return u.s
	}
	return u.s.last()
}

// Path tests if the page's url path matches the passed in value
func (u *URLMatch) Path(match string) *Sequence {
	return u.test("Path Matches", func() error {
		if u.url.Path != match {
			return fmt.Errorf("URL's path does not match %s, got %s", match, u.url.Path)
		}
		return nil
	})
}

// QueryValue tests if the page's url contains the url query matches the value
func (u *URLMatch) QueryValue(key, value string) *Sequence {
	return u.test("Query Value Matches", func() error {
		values := u.url.Query()
		if v, ok := values[key]; ok {
			found := false
			for i := range v {
				if v[i] == value {
					found = true
					break
				}

			}
			if !found {
				return fmt.Errorf("URL does not contain the value '%s' for the key '%s'. Values: %s",
					value, key, v)
			}
			return nil
		}

		return fmt.Errorf("URL does not contain the query key '%s'. URL: %s", key, u.url)
	})
}

// Fragment tests if the page's url fragment (#) matches the passed in value
func (u *URLMatch) Fragment(match string) *Sequence {
	return u.test("Fragment Matches", func() error {
		if u.url.Fragment != match {
			return fmt.Errorf("URL's fragment does not match %s, got %s", match, u.url.Fragment)
		}
		return nil
	})
}

// URL tests against the current page URL
func (s *Sequence) URL() *URLMatch {
	return &URLMatch{
		s: s,
	}
}

// Forward moves forward in the browser's history
func (s *Sequence) Forward() *Sequence {
	s.last = func() *Sequence {
		if s.err != nil {
			return s
		}

		err := s.driver.Forward()
		if err != nil {
			s.err = &Error{
				Stage:  "Forward",
				Err:    err,
				Caller: caller(1),
			}
		}
		return s
	}
	return s.last()
}

// Back moves back in the browser's history
func (s *Sequence) Back() *Sequence {
	s.last = func() *Sequence {
		if s.err != nil {
			return s
		}

		err := s.driver.Back()
		if err != nil {
			s.err = &Error{
				Stage:  "Back",
				Err:    err,
				Caller: caller(1),
			}
		}
		return s
	}
	return s.last()
}

// Refresh refreshes the page
func (s *Sequence) Refresh() *Sequence {
	s.last = func() *Sequence {
		if s.err != nil {
			return s
		}

		err := s.driver.Refresh()
		if err != nil {
			s.err = &Error{
				Stage:  "Refresh",
				Err:    err,
				Caller: caller(1),
			}
		}
		return s
	}
	return s.last()
}

// Find returns a selection of one or more elements to apply a set of actions against
// If .Any or.All are not specified, then it is assumed that the selection will contain a single element
// and the tests will fail if more than one element is found
func (s *Sequence) Find(selector string) *Elements {
	e := &Elements{
		seq:      s,
		selector: selector,
		selectFunc: func(selector string) ([]selenium.WebElement, error) {
			return s.driver.FindElements(selenium.ByCSSSelector, selector)
		},
	}

	if s.err != nil {
		return e
	}

	e.last = func() *Elements {
		var err error
		e.elems, err = e.selectFunc(selector)

		if err != nil {
			s.err = &Error{
				Stage:  "Elements",
				Err:    err,
				Caller: caller(1),
			}
			return e
		}
		return e
	}
	return e.last()
}

// Wait will wait for the given duration before continuing in the sequence
func (s *Sequence) Wait(duration time.Duration) *Sequence {
	if s.err != nil {
		return s
	}
	time.Sleep(duration)
	return s
}

// Debug will print the current page's title and source
// For use with debugging issues mostly
func (s *Sequence) Debug() *Sequence {
	src, err := s.driver.PageSource()
	if err != nil {
		s.err = &Error{
			Stage:  "Debug Source",
			Err:    err,
			Caller: caller(0),
		}
		return s
	}

	title, err := s.driver.Title()
	if err != nil {
		s.err = &Error{
			Stage:  "Debug Title",
			Err:    err,
			Caller: caller(0),
		}
		return s
	}

	uri, err := s.driver.CurrentURL()
	if err != nil {
		s.err = &Error{
			Stage:  "Debug URL",
			Err:    err,
			Caller: caller(0),
		}
		return s
	}

	// logs, err := s.driver.Log(log.Browser)
	// if err != nil {
	// 	s.err = &Error{
	// 		Stage:  "Debug Log",
	// 		Err:    err,
	// 		Caller: caller(0),
	// 	}
	// 	return s
	// }
	// log := ""
	// for i := range logs {
	// 	log += fmt.Sprintf("%s - (%s): %s\n", logs[i].Level, logs[i].Timestamp.Format(time.Stamp), logs[i].Message)
	// }

	fmt.Println("-----------------------------------------------")
	fmt.Printf("%s - (%s)\n", title, uri)
	fmt.Println("-----------------------------------------------")
	fmt.Println(src)
	fmt.Println("-----------------------------------------------")
	// fmt.Println("LOG")
	// fmt.Println(log)
	return s
}

// Screenshot takes a screenshot
func (s *Sequence) Screenshot(filename string) *Sequence {
	buff, err := s.driver.Screenshot()
	if err != nil {
		s.err = &Error{
			Stage:  "Screenshot",
			Err:    err,
			Caller: caller(1),
		}
		return s
	}

	err = ioutil.WriteFile(filename, buff, 0622)
	if err != nil {
		s.err = &Error{
			Stage: "Screenshot Writing File",
			Err:   err,
		}
		return s
	}
	return s
}

// End Completes a sequence and returns any errors
func (e *Elements) End() error {
	return e.seq.End()
}

// Ok is a shortcut for Sequence.Ok
func (e *Elements) Ok(tb testing.TB) {
	e.seq.Ok(tb)
}

// Wait sleeps for the given duration
func (e *Elements) Wait(duration time.Duration) *Elements {
	if e.seq.err != nil {
		return e
	}
	time.Sleep(duration)
	return e
}

// Any means the following tests will pass if they pass for ANY of the selected elements
func (e *Elements) Any() *Elements {
	e.all = false
	e.any = true
	return e
}

// All means the following tests will pass if they pass only if pass for ALL of the selected elements
func (e *Elements) All() *Elements {
	e.any = false
	e.all = true
	return e
}

// Count verifies that the number of elements in the selection matches the argument
func (e *Elements) Count(count int) *Elements {
	e.last = func() *Elements {
		if e.seq.err != nil {
			return e
		}

		if count != len(e.elems) {
			e.seq.err = &Error{
				Stage: "Count",
				Err: fmt.Errorf("Invalid count for selector %s wanted %d got %d", e.selector, count,
					len(e.elems)),
				Caller: caller(1),
			}

			return e
		}
		return e
	}
	return e.last()
}

// And allows you chain additional sequences
func (e *Elements) And() *Sequence {
	return e.seq
}

// Find finds a new element
func (e *Elements) Find(selector string) *Elements {
	return e.seq.Find(selector)
}

// FindChildren returns a new Elements object for all the elements that match the selector
func (e *Elements) FindChildren(selector string) *Elements {
	newE := &Elements{
		seq:      e.seq,
		selector: selector,
		selectFunc: func(selector string) ([]selenium.WebElement, error) {
			var found []selenium.WebElement
			success := false
			var lastErr error
			var lastElement selenium.WebElement

			for i := range e.elems {
				elements, err := e.elems[i].FindElements(selenium.ByCSSSelector, selector)
				if err != nil {
					lastElement = e.elems[i]
					lastErr = err
					continue
				}
				found = append(found, elements...)
				success = true
			}
			if !success {
				// all find elements calls failed
				return nil, &Error{
					Stage:   "Find Children",
					Element: lastElement,
					Err:     lastErr,
					Caller:  caller(1),
				}
			}
			return found, nil
		},
	}
	if e.seq.err != nil {
		return e
	}

	var err error

	newE.elems, err = newE.selectFunc(selector)
	if err != nil {
		newE.seq.err = err.(*Error)
	}

	return newE
}

// Test tests an arbitrary function against all the elements in this sequence
// if the function returns an error then the test fails
func (e *Elements) Test(testName string, fn func(e selenium.WebElement) error) *Elements {
	if e.seq.err != nil {
		return e
	}
	e = e.test(testName, fn)
	if e.seq.err != nil {
		e.seq.err.Caller = caller(0)
	}
	return e
}

func (e *Elements) test(testName string, fn func(e selenium.WebElement) error) *Elements {
	stage := testName + " Test"
	e.last = func() *Elements {
		if e.seq.err != nil {
			return e
		}

		if len(e.elems) == 0 {
			e.seq.err = &Error{
				Stage:  stage,
				Err:    fmt.Errorf("No elements exist for the selector '%s'", e.selector),
				Caller: caller(2),
			}
			return e
		}
		if len(e.elems) == 1 {
			err := fn(e.elems[0])
			if err != nil {
				e.seq.err = &Error{
					Stage:   stage,
					Element: e.elems[0],
					Err:     err,
					Caller:  caller(2),
				}
			}
			return e
		}

		if !e.any && !e.all {
			e.seq.err = &Error{
				Stage: stage,
				Err: fmt.Errorf("Selector '%s' returned multiple elements but .Any() or .All() weren't specified",
					e.selector),
				Caller: caller(2),
			}
			return e
		}

		var errs Errors

		for i := range e.elems {
			err := fn(e.elems[i])
			if err != nil {
				if e.all {
					e.seq.err = &Error{
						Stage:   stage,
						Element: e.elems[i],
						Err:     fmt.Errorf("Not All elements passed: %s", err),
						Caller:  caller(2),
					}
					return e
				}
				errs = append(errs, &Error{
					Stage:   stage,
					Element: e.elems[i],
					Err:     err,
					Caller:  caller(2),
				})
			} else if e.any {
				return e
			}
		}
		if len(errs) != 0 {
			e.seq.err = &Error{
				Stage:  stage,
				Err:    fmt.Errorf("None of the elements passed: %s", errs),
				Caller: caller(2),
			}

		}
		return e
	}
	return e.last()
}

// Visible tests if the elements are visible
func (e *Elements) Visible() *Elements {
	return e.test("Visible", func(we selenium.WebElement) error {
		ok, err := we.IsDisplayed()
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("Element was not visible")
		}
		return nil
	})
}

// Hidden tests if the elements are hidden
func (e *Elements) Hidden() *Elements {
	return e.test("Hidden", func(we selenium.WebElement) error {
		ok, err := we.IsDisplayed()
		if err != nil {
			return err
		}
		if ok {
			return errors.New("Element was not visible")
		}
		return nil
	})
}

// Enabled tests if the elements are hidden
func (e *Elements) Enabled() *Elements {
	return e.test("Enabled", func(we selenium.WebElement) error {
		ok, err := we.IsEnabled()
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("Element was not enabled")
		}
		return nil
	})
}

// Disabled tests if the elements are hidden
func (e *Elements) Disabled() *Elements {
	return e.test("Disabled", func(we selenium.WebElement) error {
		ok, err := we.IsEnabled()
		if err != nil {
			return err
		}
		if ok {
			return errors.New("Element was not disabled")
		}
		return nil
	})
}

// Selected tests if the elements are selected
func (e *Elements) Selected() *Elements {
	return e.test("Selected", func(we selenium.WebElement) error {
		ok, err := we.IsSelected()
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("Element was not selected")
		}
		return nil
	})
}

// Unselected tests if the elements aren't selected
func (e *Elements) Unselected() *Elements {
	return e.test("Selected", func(we selenium.WebElement) error {
		ok, err := we.IsSelected()
		if err != nil {
			return err
		}
		if ok {
			return errors.New("Element was selected")
		}
		return nil
	})
}

// StringMatch is for testing the value of strings in elements
type StringMatch struct {
	testName string
	value    func(selenium.WebElement) (string, error)
	e        *Elements
}

// Equals tests if the string value matches the passed in value exactly
func (s *StringMatch) Equals(match string) *Elements {
	return s.e.test(fmt.Sprintf("%s Equals", s.testName), func(we selenium.WebElement) error {
		val, err := s.value(we)
		if err != nil {
			return err
		}
		if val != match {
			return fmt.Errorf("The element's %s does not equal '%s'. Got '%s'", s.testName, match, val)
		}
		return nil
	})
}

// Contains tests if the string value contains the passed in value
func (s *StringMatch) Contains(match string) *Elements {
	return s.e.test(fmt.Sprintf("%s Contains", s.testName), func(we selenium.WebElement) error {
		val, err := s.value(we)
		if err != nil {
			return err
		}
		if !strings.Contains(val, match) {
			return fmt.Errorf("The Element's %s does not contain '%s'. Got '%s'", s.testName, match, val)
		}
		return nil
	})
}

// StartsWith tests if the string value starts with the passed in value
func (s *StringMatch) StartsWith(match string) *Elements {
	return s.e.test(fmt.Sprintf("%s Starts With", s.testName), func(we selenium.WebElement) error {
		val, err := s.value(we)
		if err != nil {
			return err
		}
		if !strings.HasPrefix(val, match) {
			return fmt.Errorf("The Element's %s does not start with '%s'. Got '%s'", s.testName, match, val)
		}
		return nil
	})
}

// EndsWith tests if the string value end with the passed in value
func (s *StringMatch) EndsWith(match string) *Elements {
	return s.e.test(fmt.Sprintf("%s Ends With", s.testName), func(we selenium.WebElement) error {
		val, err := s.value(we)
		if err != nil {
			return err
		}
		if !strings.HasSuffix(val, match) {
			return fmt.Errorf("The Element's %s does not end with '%s'. Got '%s'", s.testName, match, val)
		}
		return nil
	})
}

// Regexp tests if the string value matches the regular expression
func (s *StringMatch) Regexp(exp *regexp.Regexp) *Elements {
	return s.e.test(fmt.Sprintf("%s Matches RegExp", s.testName), func(we selenium.WebElement) error {
		val, err := s.value(we)
		if err != nil {
			return err
		}
		if !exp.MatchString(val) {
			return fmt.Errorf("The Element's %s does not match the regex '%s'.", s.testName, exp)
		}
		return nil
	})
}

// TagName tests if the elements match the given tag name
func (e *Elements) TagName() *StringMatch {
	return &StringMatch{
		testName: "TagName",
		value: func(we selenium.WebElement) (string, error) {
			return we.TagName()
		},
		e: e,
	}
}

// Text tests if the elements matches
func (e *Elements) Text() *StringMatch {
	return &StringMatch{
		testName: "Text",
		value: func(we selenium.WebElement) (string, error) {
			return we.Text()
		},
		e: e,
	}
}

// Attribute tests if the elements attribute matches
func (e *Elements) Attribute(attribute string) *StringMatch {
	return &StringMatch{
		testName: fmt.Sprintf("%s Attribute", attribute),
		value: func(we selenium.WebElement) (string, error) {
			return we.GetAttribute(attribute)
		},
		e: e,
	}
}

// CSSProperty tests if the elements attribute matches
func (e *Elements) CSSProperty(property string) *StringMatch {
	return &StringMatch{
		testName: fmt.Sprintf("%s CSS Property", property),
		value: func(we selenium.WebElement) (string, error) {
			return we.CSSProperty(property)
		},
		e: e,
	}
}

// Click sends a click to all of the elements
func (e *Elements) Click() *Elements {
	return e.test("Click", func(we selenium.WebElement) error {
		return we.Click()
	})
}

// SendKeys sends a string of key to the elements
func (e *Elements) SendKeys(keys string) *Elements {
	return e.test("SendKeys", func(we selenium.WebElement) error {
		return we.SendKeys(keys)
	})
}

// Submit sends a submit event to the elements
func (e *Elements) Submit() *Elements {
	return e.test("Submit", func(we selenium.WebElement) error {
		return we.Submit()
	})
}

// Clear clears the elements
func (e *Elements) Clear() *Elements {
	return e.test("Clear", func(we selenium.WebElement) error {
		return we.Clear()
	})
}

// Filter filters out any elements for which the passed in function returns an error, useful for
// matching elements by text contents, since they can't be selected for with css selectors
func (e *Elements) Filter(fn func(we *Elements) error) *Elements {
	if e.seq.err != nil {
		return e
	}

	var filtered []selenium.WebElement

	for i := range e.elems {
		// run filter tests on copies of sequence and elements, so errors, and last funcs don't get propogated
		we := &Elements{
			seq: &Sequence{
				driver:          e.seq.driver,
				EventualPoll:    e.seq.EventualPoll,
				EventualTimeout: e.seq.EventualTimeout,
			},
			elems: []selenium.WebElement{e.elems[i]},
		}
		err := fn(we)
		if err == nil {
			filtered = append(filtered, e.elems[i])
		}
	}

	e.elems = filtered
	return e
}
