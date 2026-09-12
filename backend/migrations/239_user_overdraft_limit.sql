ALTER TABLE users
    ADD COLUMN IF NOT EXISTS overdraft_limit DECIMAL(20,8)
    CHECK (overdraft_limit >= 0 AND overdraft_limit <= 999999999999);

COMMENT ON COLUMN users.overdraft_limit IS 'Credit for balance-billed requests: NULL inherits the site default; zero disables overdraft.';
