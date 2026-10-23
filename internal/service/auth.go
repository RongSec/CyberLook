package service

import (
	"strings"
	"errors"
	"log"
	"time"

	"asset-platform/internal/config"
	"asset-platform/internal/model"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

// InitDefaultUser 创建默认管理员（admin/admin123），已有用户则跳过
func InitDefaultUser() error {
	var count int64
	if err := config.DB.Model(&model.User{}).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("admin123"), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	return config.DB.Create(&model.User{Username: "admin", PasswordHash: string(hash), Role: "admin"}).Error
}

// Login 校验用户名密码，返回 JWT token
func Login(username, password, ip string) (string, error) {
	if err := CheckLoginLock(username, ip); err != nil {
		return "", err
	}
	var user model.User
	if err := config.DB.Where("username = ?", username).First(&user).Error; err != nil {
		log.Printf("[login] 查询失败 username=%q err=%v", username, err)
		RecordLoginFail(username, ip)
		return "", errors.New("用户名或密码错误")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
		log.Printf("[login] 密码校验失败 username=%q hashLen=%d err=%v", username, len(user.PasswordHash), err)
		RecordLoginFail(username, ip)
		return "", errors.New("用户名或密码错误")
	}
	ClearLoginFail(username, ip)
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"uid":      user.ID,
		"username": user.Username,
		"role":     user.Role,
		"exp":      time.Now().Add(24 * time.Hour).Unix(),
	})
	return token.SignedString([]byte(config.JWTSecret()))
}

// ChangePassword 修改密码（校验旧密码）
func ChangePassword(username, oldPwd, newPwd string) error {
	var user model.User
	if err := config.DB.Where("username = ?", username).First(&user).Error; err != nil {
		return errors.New("用户不存在")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(oldPwd)); err != nil {
		return errors.New("旧密码错误")
	}
	if err := validatePasswordStrength(newPwd); err != nil {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPwd), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	return config.DB.Model(&user).Update("password_hash", string(hash)).Error
}

// validatePasswordStrength 密码强度校验
// 规则：至少 8 位；大写/小写/数字/符号四类中至少三类；不在常见弱口令表内
func validatePasswordStrength(pwd string) error {
	if len([]rune(pwd)) < 8 {
		return errors.New("密码至少 8 位")
	}
	var upper, lower, digit, symbol bool
	for _, r := range pwd {
		switch {
		case r >= 'A' && r <= 'Z':
			upper = true
		case r >= 'a' && r <= 'z':
			lower = true
		case r >= '0' && r <= '9':
			digit = true
		default:
			symbol = true
		}
	}
	kinds := 0
	for _, ok := range []bool{upper, lower, digit, symbol} {
		if ok {
			kinds++
		}
	}
	if kinds < 3 {
		return errors.New("密码需包含大写字母、小写字母、数字、符号中的至少三类")
	}
	weak := []string{
		"12345678", "123456789", "1234567890", "password", "password1", "passw0rd",
		"admin123", "admin1234", "qwerty123", "abc12345", "11111111", "88888888",
		"a1234567", "1qaz2wsx", "zaq12wsx", "iloveyou",
	}
	lp := strings.ToLower(pwd)
	for _, w := range weak {
		if lp == w {
			return errors.New("密码过于简单，请更换")
		}
	}
	return nil
}
