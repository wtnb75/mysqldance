-- Sample data for trying mysqldance by hand (task sample:up).
-- Every value is fixed: no NOW(), RAND() or UUID(), so the data is the same
-- every time it is created.
SET NAMES utf8mb4;

DROP DATABASE IF EXISTS sample_main;
CREATE DATABASE sample_main DEFAULT CHARACTER SET utf8mb4;
USE sample_main;

-- Strings that need escaping: newline, quotes, backslash, NUL, Japanese, emoji.
CREATE TABLE strings (
  id INT NOT NULL,
  label VARCHAR(40) NOT NULL,
  body TEXT,
  PRIMARY KEY (id)
);
INSERT INTO strings (id, label, body) VALUES
  (1, 'plain', 'hello world'),
  (2, 'newline', CONCAT('line1', CHAR(10), 'line2', CHAR(13), CHAR(10), 'line3')),
  (3, 'single quote', 'it''s'),
  (4, 'double quote', 'say "hi"'),
  (5, 'backslash', 'C:\\dir\\file\\n'),
  (6, 'nul', CONCAT('before', CHAR(0), 'after')),
  (7, 'japanese', 'こんにちは、世界'),
  (8, 'emoji', '😀 🚀 👨‍👩‍👧'),
  (9, 'empty', ''),
  (10, 'null body', NULL),
  (11, 'tab and control', CONCAT('a', CHAR(9), 'b', CHAR(26), 'c')),
  (12, 'mixed', CONCAT('it''s "all" \\ ', CHAR(10), 'の 😀'));

-- Binary types.
CREATE TABLE bins (
  id INT NOT NULL,
  b BLOB,
  vb VARBINARY(32),
  fixed BINARY(4),
  bits BIT(10),
  PRIMARY KEY (id)
);
INSERT INTO bins (id, b, vb, fixed, bits) VALUES
  (1, 0x00010203FF, 0x41420043, 0xDEADBEEF, b'1010101010'),
  (2, '', '', 0x00000000, b'0'),
  (3, NULL, NULL, NULL, NULL),
  (4, 0x27225C0A0D, 0x5C5C, 0x5C275C22, b'1111111111');

-- A table without a key, with duplicate rows.
CREATE TABLE nokey (
  name VARCHAR(20),
  n INT
);
INSERT INTO nokey (name, n) VALUES
  ('dup', 1),
  ('dup', 1),
  ('b', 2),
  (NULL, NULL),
  ('a', 3),
  ('a', 3);

-- A generated column and an INVISIBLE column.
CREATE TABLE gen_invisible (
  id INT NOT NULL,
  price DECIMAL(10,2) NOT NULL,
  qty INT NOT NULL,
  total DECIMAL(12,2) GENERATED ALWAYS AS (price * qty) STORED,
  label VARCHAR(30) GENERATED ALWAYS AS (CONCAT('item-', id)) VIRTUAL,
  note VARCHAR(40) INVISIBLE,
  PRIMARY KEY (id)
);
INSERT INTO gen_invisible (id, price, qty, note) VALUES
  (1, 9.99, 3, 'hidden one'),
  (2, 0.50, 10, NULL),
  (3, 1234567.89, 1, 'big');

-- Foreign keys: a parent and a child.
CREATE TABLE parent (
  id INT NOT NULL,
  name VARCHAR(20) NOT NULL,
  PRIMARY KEY (id)
);
CREATE TABLE child (
  id INT NOT NULL,
  parent_id INT NOT NULL,
  name VARCHAR(20) NOT NULL,
  PRIMARY KEY (id),
  CONSTRAINT child_parent_fk FOREIGN KEY (parent_id) REFERENCES parent (id)
);
INSERT INTO parent (id, name) VALUES (1, 'p1'), (2, 'p2');
INSERT INTO child (id, parent_id, name) VALUES (1, 1, 'c1'), (2, 1, 'c2'), (3, 2, 'c3');

-- A cycle of foreign keys: cyc_a -> cyc_b -> cyc_a.
CREATE TABLE cyc_a (
  id INT NOT NULL,
  b_id INT,
  PRIMARY KEY (id)
);
CREATE TABLE cyc_b (
  id INT NOT NULL,
  a_id INT,
  PRIMARY KEY (id)
);
INSERT INTO cyc_a (id, b_id) VALUES (1, NULL), (2, NULL);
INSERT INTO cyc_b (id, a_id) VALUES (10, 1), (20, 2);
UPDATE cyc_a SET b_id = 10 WHERE id = 1;
UPDATE cyc_a SET b_id = 20 WHERE id = 2;
ALTER TABLE cyc_a ADD CONSTRAINT cyc_a_b_fk FOREIGN KEY (b_id) REFERENCES cyc_b (id);
ALTER TABLE cyc_b ADD CONSTRAINT cyc_b_a_fk FOREIGN KEY (a_id) REFERENCES cyc_a (id);

-- Other column types with fixed values.
CREATE TABLE misc (
  id INT NOT NULL,
  d DATE,
  dt DATETIME(3),
  ts TIMESTAMP NULL,
  t TIME(2),
  dec_col DECIMAL(20,6),
  dbl DOUBLE,
  e ENUM('small', 'medium', 'large'),
  s SET('a', 'b', 'c'),
  j JSON,
  PRIMARY KEY (id)
);
SET time_zone = '+00:00';
INSERT INTO misc (id, d, dt, ts, t, dec_col, dbl, e, s, j) VALUES
  (1, '2024-02-29', '2024-02-29 12:34:56.789', '2024-01-01 00:00:01', '-12:34:56.78', 12345678901234.123456, 1.5, 'medium', 'a,c', '{"k": [1, 2, {"x": "日本語"}], "n": null}'),
  (2, '1000-01-01', '9999-12-31 23:59:59.999', NULL, '00:00:00.00', -0.000001, -2.25e-300, 'large', '', '[]'),
  (3, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL);

-- Two levels of views.
CREATE VIEW v_child_names AS
  SELECT c.id AS child_id, c.name AS child_name, p.name AS parent_name
  FROM child AS c JOIN parent AS p ON p.id = c.parent_id;
CREATE VIEW v_parent_summary AS
  SELECT parent_name, COUNT(*) AS children
  FROM v_child_names
  GROUP BY parent_name;
