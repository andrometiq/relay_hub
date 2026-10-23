ALTER TABLE relay_callback_cursor ADD COLUMN status_offset integer NOT NULL DEFAULT 0 CHECK (status_offset >= 0);
