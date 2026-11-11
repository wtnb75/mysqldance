-- A second database, so that dumps with several databases can be tried.
SET NAMES utf8mb4;

DROP DATABASE IF EXISTS sample_second;
CREATE DATABASE sample_second DEFAULT CHARACTER SET utf8mb4;
USE sample_second;

CREATE TABLE notes (
  id INT NOT NULL,
  note VARCHAR(100) NOT NULL,
  PRIMARY KEY (id)
);
INSERT INTO notes (id, note) VALUES
  (1, 'first'),
  (2, 'second'),
  (3, 'third');

-- The same table name as in sample_main, with different content.
CREATE TABLE strings (
  id INT NOT NULL,
  body VARCHAR(50),
  PRIMARY KEY (id)
);
INSERT INTO strings (id, body) VALUES (1, 'other database');
