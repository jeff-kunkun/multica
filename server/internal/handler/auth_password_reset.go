package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/logger"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"golang.org/x/crypto/bcrypt"
)

// Password reset: POST /auth/reset-password, the forgot-password flow. Username + the
//     team 2FA code (MULTICA_SIGNUP_TOTP_SECRET, the same one signup uses) +
//     a new password. No email involved — most self-host accounts carry a
//     placeholder address.
//
// Existing sessions are left alone on purpose. UI JWTs are stateless (see
// auth/session.go) so there is nothing to revoke without adding a DB read to
// every request, and revoking PATs would silently stop the member's daemons.
// Only accounts in user_password_credential can be reset; the env bootstrap
// account (MULTICA_PASSWORD_AUTH_*) lives in configuration, not here.

// passwordResetRejectedMessage is the one answer the forgot-password flow gives
// for an unknown username, a wrong code, or a reused code, so a caller cannot
// tell which part was wrong.
const passwordResetRejectedMessage = "invalid username or team 2FA code"

type PasswordResetRequest struct {
	Username    string `json:"username"`
	Totp        string `json:"totp"`
	NewPassword string `json:"new_password"`
}

func validNewPassword(password string) bool {
	return utf8.RuneCountInString(password) >= minPasswordSignupPasswordLen && len(password) <= maxPasswordLoginPasswordLen
}

func (h *Handler) PasswordReset(w http.ResponseWriter, r *http.Request) {
	creds, ok := passwordAuthConfigured()
	if !ok {
		writeError(w, http.StatusNotFound, "password reset is not enabled")
		return
	}
	totpSecret, totpRequired := signupTOTPSecret()
	if !totpRequired {
		// Without the team 2FA gate a username alone would be enough to take
		// over an account.
		writeError(w, http.StatusNotFound, "password reset is not enabled")
		return
	}

	var req PasswordResetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	username := strings.TrimSpace(req.Username)
	if username == "" || len(username) > maxPasswordLoginUsernameLen || strings.TrimSpace(req.Totp) == "" {
		writeError(w, http.StatusBadRequest, "username, team 2FA code and new password are required")
		return
	}
	if !validNewPassword(req.NewPassword) {
		writeError(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}

	now := time.Now()
	if _, ok := verifySignupTOTP(totpSecret, req.Totp, now); !ok {
		writeError(w, http.StatusUnauthorized, passwordResetRejectedMessage)
		return
	}
	if secretEqual(username, creds.username) {
		writeError(w, http.StatusUnauthorized, passwordResetRejectedMessage)
		return
	}
	rec, err := h.Queries.GetPasswordCredentialByUsername(r.Context(), username)
	if err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusUnauthorized, passwordResetRejectedMessage)
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to reset password")
		return
	}

	err = h.applyPasswordReset(r.Context(), rec.UserID, req.NewPassword, func(qtx *db.Queries) error {
		return consumeSignupTOTP(r.Context(), qtx, totpSecret, req.Totp, now)
	})
	if err != nil {
		if errors.Is(err, errSignupTOTPRejected) {
			writeError(w, http.StatusUnauthorized, passwordResetRejectedMessage)
			return
		}
		slog.Warn("password reset failed", append(logger.RequestAttrs(r), "error", err, "user_id", uuidToString(rec.UserID))...)
		writeError(w, http.StatusInternalServerError, "failed to reset password")
		return
	}

	slog.Info("password reset via team 2FA", append(logger.RequestAttrs(r), "user_id", uuidToString(rec.UserID))...)
	w.WriteHeader(http.StatusNoContent)
}

// applyPasswordReset writes the new hash. guard runs inside the same
// transaction so a consumed 2FA step and the password change land or roll
// back together.
func (h *Handler) applyPasswordReset(ctx context.Context, userID pgtype.UUID, password string, guard func(*db.Queries) error) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	qtx := h.Queries.WithTx(tx)
	if guard != nil {
		if err := guard(qtx); err != nil {
			return err
		}
	}
	if _, err := qtx.UpdatePasswordCredentialHash(ctx, db.UpdatePasswordCredentialHashParams{
		UserID:       userID,
		PasswordHash: string(hash),
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
