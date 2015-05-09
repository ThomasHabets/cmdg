package main

import (
	"encoding/xml"
	"fmt"
	"io/ioutil"
	"time"
)

type contactEmail struct {
	Primary bool   `xml:"primary,attr"`
	Rel     string `xml:"rel,attr"`
	Email   string `xml:"address,attr"`
}
type contactEntry struct {
	ID    string         `xml:"id"`
	Title string         `xml:"title"`
	Email []contactEmail `xml:"email"`
}
type contactsT struct {
	ID    string         `xml:"id"`
	Title string         `xml:"title"`
	Entry []contactEntry `xml:"entry"`
}

func updateContacts() error {
	c, err := getContacts()
	if err != nil {
		return err
	}
	contacts = c
	return nil
}

func contactAddresses() []string {
	var ret []string
	for _, c := range contacts.Entry {
		for _, e := range c.Email {
			if c.Title != "" {
				ret = append(ret, fmt.Sprintf("%s <%s>", c.Title, e.Email))
			} else {
				ret = append(ret, e.Email)
			}
		}
	}
	return ret
}

func getContacts() (contactsT, error) {
	st := time.Now()
	resp, err := authedClient.Get("https://www.google.com/m8/feeds/contacts/default/full")
	if err != nil {
		return contactsT{}, fmt.Errorf("getting contacts: %v", err)
	}
	defer resp.Body.Close()
	b, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return contactsT{}, fmt.Errorf("reading contacts: %v", err)
	}
	profileAPI("Contacts.Get", time.Since(st))
	var c contactsT
	if err := xml.Unmarshal(b, &c); err != nil {
		return contactsT{}, fmt.Errorf("decoding contacts XML: %v", err)
	}
	return c, nil
}
