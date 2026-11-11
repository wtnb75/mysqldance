-- Big tables for measuring mysqldance (B-017, B-018, B-044, B-045).
--
-- Loaded by `task sample:big`, which first sets these session variables:
--   @rows        rows per table
--   @blob_bytes  size of the BLOB column of the "large" tables, in bytes
--   @short       1 to create the "short" tables (small rows), else 0
--   @large       1 to create the "large" tables (rows with a BLOB), else 0
--
-- The rows are generated on the server from the row number alone (no NOW(),
-- RAND() or UUID()), so the same variables always give the same data.
--
-- Tables, by key type:
--   int_*    BIGINT primary key (1..@rows, in insertion order)
--   str_*    CHAR(32) primary key, MD5 of the row number (unordered)
--   nokey_*  no key at all
SET NAMES utf8mb4;

DROP DATABASE IF EXISTS sample_big;
CREATE DATABASE sample_big DEFAULT CHARACTER SET utf8mb4;
USE sample_big;

DELIMITER //

CREATE PROCEDURE fill_big()
BEGIN
  DECLARE chunk BIGINT DEFAULT 10000;
  DECLARE have BIGINT DEFAULT 1;
  DECLARE lo BIGINT DEFAULT 1;
  DECLARE hi BIGINT;

  -- A table of 1..@rows, built by doubling.
  CREATE TABLE seq (n BIGINT UNSIGNED NOT NULL, PRIMARY KEY (n));
  INSERT INTO seq VALUES (1);
  WHILE have < @rows DO
    INSERT INTO seq SELECT n + have FROM seq WHERE n + have <= @rows;
    SET have = have * 2;
  END WHILE;

  IF @short = 1 THEN
    CREATE TABLE int_short (
      id BIGINT NOT NULL,
      a INT NOT NULL,
      b VARCHAR(32) NOT NULL,
      PRIMARY KEY (id)
    );
    CREATE TABLE str_short (
      k CHAR(32) NOT NULL,
      a INT NOT NULL,
      b VARCHAR(32) NOT NULL,
      PRIMARY KEY (k)
    );
    CREATE TABLE nokey_short (
      id BIGINT NOT NULL,
      a INT NOT NULL,
      b VARCHAR(32) NOT NULL
    );
  END IF;

  IF @large = 1 THEN
    CREATE TABLE int_large (
      id BIGINT NOT NULL,
      a INT NOT NULL,
      note VARCHAR(100) NOT NULL,
      payload MEDIUMBLOB NOT NULL,
      PRIMARY KEY (id)
    );
    CREATE TABLE str_large (
      k CHAR(32) NOT NULL,
      a INT NOT NULL,
      note VARCHAR(100) NOT NULL,
      payload MEDIUMBLOB NOT NULL,
      PRIMARY KEY (k)
    );
    CREATE TABLE nokey_large (
      id BIGINT NOT NULL,
      a INT NOT NULL,
      note VARCHAR(100) NOT NULL,
      payload MEDIUMBLOB NOT NULL
    );
  END IF;

  -- Insert in chunks so that no transaction (undo log) grows with @rows.
  WHILE lo <= @rows DO
    SET hi = LEAST(lo + chunk - 1, @rows);
    IF @short = 1 THEN
      INSERT INTO int_short
        SELECT n, (n * 7919) % 1000003, CONCAT('name-', n % 1000)
        FROM seq WHERE n BETWEEN lo AND hi;
      INSERT INTO str_short
        SELECT MD5(n), (n * 7919) % 1000003, CONCAT('name-', n % 1000)
        FROM seq WHERE n BETWEEN lo AND hi;
      INSERT INTO nokey_short
        SELECT n, (n * 7919) % 1000003, CONCAT('name-', n % 1000)
        FROM seq WHERE n BETWEEN lo AND hi;
    END IF;
    IF @large = 1 THEN
      INSERT INTO int_large
        SELECT n, (n * 7919) % 1000003, CONCAT('note-', n),
               LEFT(UNHEX(REPEAT(MD5(n), CEIL(@blob_bytes / 16))), @blob_bytes)
        FROM seq WHERE n BETWEEN lo AND hi;
      INSERT INTO str_large
        SELECT MD5(n), (n * 7919) % 1000003, CONCAT('note-', n),
               LEFT(UNHEX(REPEAT(MD5(n), CEIL(@blob_bytes / 16))), @blob_bytes)
        FROM seq WHERE n BETWEEN lo AND hi;
      INSERT INTO nokey_large
        SELECT n, (n * 7919) % 1000003, CONCAT('note-', n),
               LEFT(UNHEX(REPEAT(MD5(n), CEIL(@blob_bytes / 16))), @blob_bytes)
        FROM seq WHERE n BETWEEN lo AND hi;
    END IF;
    SET lo = hi + 1;
  END WHILE;

  DROP TABLE seq;
END//

DELIMITER ;

CALL fill_big();
DROP PROCEDURE fill_big;
