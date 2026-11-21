CREATE TABLE notifications(
    id UUID PRIMARY KEY,
    recipient TEXT NOT NULL,
    channel TEXT NOT NULL CHECK (channel IN ('sms','email','push')),
    content TEXT NOT NULL,
    priority SMALLINT NOT NULL DEFAULT 1,
    status TEXT NOT NULL DEFAULT 'pending',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);