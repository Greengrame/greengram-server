-- /apiws sign-ins stored Traefik's docker address. Better blank until the
-- session's next call stamps the real one.
UPDATE sessions SET ip = '' WHERE ip LIKE '10.%' OR ip LIKE '192.168.%' OR ip LIKE '127.%' OR ip LIKE 'fd%' OR ip LIKE 'fc%' OR ip = '::1'
    OR ip ~ '^172\.(1[6-9]|2[0-9]|3[01])\.';
