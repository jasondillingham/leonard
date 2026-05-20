//! Top-level fixture module, mirroring the Python/TS testdata shapes.

use std::collections::HashMap;

pub const VERSION: &str = "0.1";
const HIDDEN: u32 = 42;
pub static FLAG: bool = true;

pub fn hello(name: &str) -> String {
    format!("hello {}", name)
}

fn _helper() -> u32 {
    1
}

pub async fn fetch_all(urls: Vec<String>) -> Vec<String> {
    urls
}

pub struct Container<T> {
    pub items: Vec<T>,
}

impl<T: Clone> Container<T> {
    pub fn new(items: Vec<T>) -> Self {
        Self { items }
    }

    pub async fn load(_url: &str) -> Result<Self, std::io::Error> {
        unimplemented!()
    }

    fn _refresh(&self) -> usize {
        self.items.len()
    }
}

pub struct _Private;

pub enum Outcome<T> {
    Ok(T),
    Err(String),
}

pub trait Display {
    fn show(&self) -> String;
}

pub type Bag<T> = HashMap<String, T>;
