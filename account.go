package main

import (
	"encoding/json"
	"os"
)

// Account 账号密码（用于自动重登录）。
type Account struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func loadAccount(file string) (Account, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return Account{}, err
	}
	var a Account
	if err := json.Unmarshal(data, &a); err != nil {
		return Account{}, err
	}
	return a, nil
}

func saveAccount(file string, a Account) error {
	data, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(file, data, 0600)
}
