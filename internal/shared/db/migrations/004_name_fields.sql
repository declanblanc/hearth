-- +goose Up
ALTER TABLE users RENAME COLUMN display_name TO first_name;
ALTER TABLE users ADD COLUMN last_name TEXT NOT NULL DEFAULT '';

-- Split existing single-field names at the first space.
-- Users with no space keep their whole name in first_name.
UPDATE users
   SET last_name  = CASE WHEN instr(first_name, ' ') > 0
                         THEN substr(first_name, instr(first_name, ' ') + 1)
                         ELSE '' END,
       first_name = CASE WHEN instr(first_name, ' ') > 0
                         THEN substr(first_name, 1, instr(first_name, ' ') - 1)
                         ELSE first_name END;

-- +goose Down
ALTER TABLE users DROP COLUMN last_name;
ALTER TABLE users RENAME COLUMN first_name TO display_name;
