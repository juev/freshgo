package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/juev/freshgo/internal/store"
)

// Where the script of the login page begins and finishes a login with a
// passkey.
const (
	passkeyLoginBeginPath  = "/passkeys/login/begin"
	passkeyLoginFinishPath = "/passkeys/login/finish"
)

// beginPasskeyLogin starts a login with a passkey and answers with the
// options for the authenticator. Nobody is named: the authenticator offers
// the passkeys it holds for this server, and the one picked says who logs in.
func (h *Handler) beginPasskeyLogin(w http.ResponseWriter, r *http.Request) {
	rp, ok := h.passkeyContext(w, r)
	if !ok {
		return
	}
	var in struct {
		Next     string `json:"next"`
		Remember bool   `json:"remember"`
	}
	if !h.passkeyBody(w, r, &in) {
		return
	}
	if len(in.Next) > maxOIDCNext {
		in.Next = ""
	}
	assertion, session, err := rp.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		h.broken(w, r, err)
		return
	}
	cer := &ceremony{kind: ceremonyLogin, session: *session, next: in.Next, persistent: in.Remember}
	if h.startCeremony(w, r, cer) {
		h.answerJSON(w, r, http.StatusOK, assertion)
	}
}

// finishPasskeyLogin takes the signature of the authenticator and logs in
// the user the passkey belongs to. The signature is over the challenge this
// browser was given, which is good for one answer; the authenticator has
// verified the user, the passkey is one the user has, the user handle is
// theirs, and the counter of signatures went forward.
func (h *Handler) finishPasskeyLogin(w http.ResponseWriter, r *http.Request) {
	ctx, s := r.Context(), state(r)
	rp, ok := h.passkeyContext(w, r)
	if !ok {
		return
	}
	cer, ok := h.takeCeremony(w, r, ceremonyLogin)
	if !ok {
		h.refuse(w, http.StatusBadRequest)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxPasskeyBody)
	var (
		found     *store.Passkey
		owner     *store.User
		lookupErr error
	)
	_, credential, err := rp.FinishPasskeyLogin(func(rawID, _ []byte) (webauthn.User, error) {
		p, err := h.db.PasskeyByCredential(ctx, rawID)
		if err != nil {
			if !errors.Is(err, store.ErrNotFound) {
				lookupErr = err
			}
			return nil, err
		}
		u, err := h.db.UserByID(ctx, p.UserID)
		if err != nil {
			lookupErr = err
			return nil, err
		}
		if !readPreferences(u).enabled() {
			return nil, fmt.Errorf("user %q is kept out", u.Name)
		}
		user, err := h.loadPasskeyUser(ctx, u, false)
		if err != nil {
			lookupErr = err
			return nil, err
		}
		found, owner = p, u
		return user, nil
	}, cer.session, r)
	if lookupErr != nil {
		h.broken(w, r, lookupErr)
		return
	}
	if err != nil {
		h.log.Warn("login with a passkey refused", "error", err)
		h.refuse(w, http.StatusUnauthorized)
		return
	}
	// The counter of signatures went back or stood still: the passkey may
	// have been copied, and the copy is as good as the original.
	if credential.Authenticator.CloneWarning {
		h.log.Warn("login with a passkey refused: its counter of signatures did not go forward", "user", owner.Name, "passkey", found.Name)
		h.refuse(w, http.StatusUnauthorized)
		return
	}
	data, err := json.Marshal(credential)
	if err == nil {
		err = h.db.UsePasskey(ctx, found.ID, data, h.now().Unix())
	}
	if err != nil {
		h.broken(w, r, err)
		return
	}
	// The login the browser had ends: who signs in now may be another user.
	if s.who != nil && s.who.session != nil {
		if err := h.db.DeleteSession(ctx, s.who.session.TokenHash); err != nil {
			h.broken(w, r, err)
			return
		}
		h.loginsEnded(s.who.user.ID)
	}
	if err := h.startSession(w, r, owner, cer.persistent); err != nil {
		h.broken(w, r, err)
		return
	}
	h.answerJSON(w, r, http.StatusOK, map[string]string{"next": h.localTarget(cer.next)})
}
