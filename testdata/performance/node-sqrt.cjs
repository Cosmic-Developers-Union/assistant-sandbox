console.time('bench');
let s = 0;
for (let i = 0; i < 1e8; i++) s += Math.sqrt(i);
console.timeEnd('bench');
console.log(s);
