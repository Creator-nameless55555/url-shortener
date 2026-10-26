CREATE TABLE IF NOT EXISTS links (
    code text COLLATE "C" PRIMARY KEY,
    original_url text COLLATE "C" NOT NULL UNIQUE,
    CONSTRAINT code_format CHECK (code ~ '^[a-zA-Z0-9_]{10}$'),
    CONSTRAINT url_size CHECK (octet_length(original_url) BETWEEN 1 AND 2048)
);
