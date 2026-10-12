package grpc

import (
	"encoding/base64"
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type ProtobufClaim struct {
	jwt.RegisteredClaims
	Payload string `json:"payload"`
}

func (a *AutograderService) signPayloadToken(key []byte, payload proto.Message, expireAt time.Time) (string, error) {
	raw, err := proto.Marshal(payload)
	if err != nil {
		return "", status.Error(codes.Internal, CodeProtobufMarshal)
	}
	now := time.Now()
	claims := ProtobufClaim{
		Payload: base64.StdEncoding.EncodeToString(raw),
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expireAt),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS512, claims)
	ss, err := token.SignedString(key)
	if err != nil {
		return "", status.Error(codes.Internal, CodeJwtSign)
	}
	return ss, nil
}

func (a *AutograderService) parseTokenPayload(key []byte, tokenString string) ([]byte, error) {
	token, err := jwt.Parse(
		tokenString,
		func(token *jwt.Token) (interface{}, error) {
			return key, nil
		},
		// Pin the accepted algorithm instead of trusting the token header;
		// Parse also enforces the exp claim.
		jwt.WithValidMethods([]string{jwt.SigningMethodHS512.Alg()}),
	)
	if err != nil {
		return nil, err
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok || !token.Valid {
		return nil, errors.New("claim not valid")
	}
	payloadString, ok := claims["payload"].(string)
	if !ok {
		return nil, errors.New("payload not found")
	}

	payload, err := base64.StdEncoding.DecodeString(payloadString)
	if err != nil {
		return nil, err
	}
	return payload, nil
}
