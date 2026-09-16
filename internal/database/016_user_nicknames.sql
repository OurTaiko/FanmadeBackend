ALTER TABLE users ADD COLUMN nickname text;
UPDATE users SET nickname=username;
ALTER TABLE users ALTER COLUMN nickname SET NOT NULL;
ALTER TABLE users ADD CONSTRAINT users_nickname_check CHECK (
 char_length(nickname) BETWEEN 1 AND 40 AND octet_length(nickname)<=160
 AND btrim(nickname)<>'' AND nickname !~ '[[:cntrl:]]'
);

-- Preserve legacy login names while enforcing the new rule for new/changed names.
-- Imports that omit a nickname get the same default as registration.
CREATE FUNCTION prepare_user_profile() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='INSERT' THEN
  IF NEW.nickname IS NULL THEN NEW.nickname := NEW.username; END IF;
 ELSIF NEW.username IS NOT DISTINCT FROM OLD.username THEN
  RETURN NEW;
 END IF;
 IF NEW.username !~ '^[A-Za-z0-9]{3,24}$' THEN
  RAISE EXCEPTION 'username must contain 3-24 ASCII letters or digits' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER users_prepare_profile BEFORE INSERT OR UPDATE OF username ON users
 FOR EACH ROW EXECUTE FUNCTION prepare_user_profile();
