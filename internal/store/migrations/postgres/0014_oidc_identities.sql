-- Who a user is at an OpenID Connect provider: the issuer and the subject of
-- its ID tokens. A subject belongs to one user, and a user has one subject at
-- an issuer.
CREATE TABLE oidc_identities (
	issuer TEXT NOT NULL,
	subject TEXT NOT NULL,
	user_id BIGINT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
	PRIMARY KEY (issuer, subject),
	UNIQUE (user_id, issuer)
);
