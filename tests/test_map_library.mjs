import assert from 'node:assert/strict';
import {buildMapLibrary,filterMapLibrary} from '../web/map-library.js';

const row = (map,build,name,base='C:/Exports') => ({path:`${base}/Maps/${map}/${build}/${name}.owmap`, name, relativePath:`Maps\\${map}\\${build}\\${name}.owmap`});
const rows = [row('Colosseo','B10','Night - Push'),row('Colosseo','B2','Morning - Skirmish'),
  row('Colosseo','B2','Night - Push'),row("King's Row (Winter)",'6B1','Evening - Hybrid'),
  row('東京','1','夜 - Hybrid'),row('Colosseo','B2','Variant 10'),row('Colosseo','B2','Variant 2')];
const original=JSON.stringify(rows), tree=buildMapLibrary(rows);
assert.deepEqual(tree.find(node=>node.name==='Colosseo').children.map(node=>node.name),['B2','B10']);
const colosseo=tree.find(node=>node.name==='Colosseo');
assert.deepEqual(colosseo.children[0].children.map(node=>node.name),['Morning - Skirmish','Night - Push','Variant 2','Variant 10']);
assert.equal(colosseo.count,5);
assert.equal(colosseo.children[0].children[1].row.path,rows[2].path);
assert.equal(tree.find(node=>node.name==="King's Row (Winter)").children[0].name,'6B1');
assert.equal(JSON.stringify(rows),original,'Grouping does not alter source rows or paths.');
assert.equal(filterMapLibrary(tree,'colosseo night').length,1);
assert.equal(filterMapLibrary(tree,'colosseo night')[0].count,2,'Identical names in separate variant folders stay distinct.');
assert.equal(filterMapLibrary(tree,'東京 夜')[0].count,1);
assert.deepEqual(filterMapLibrary(tree,'does not exist'),[]);
assert.equal(filterMapLibrary(tree,''),tree);
const selectedRoot=buildMapLibrary([{...rows[3],relativePath:'6B1/Evening - Hybrid.owmap'}]);
assert.equal(selectedRoot[0].name,"King's Row (Winter)",'Scanning one map keeps its name from the exported path.');
assert.equal(selectedRoot[0].children[0].name,'6B1');
assert.equal(buildMapLibrary([{...rows[3],path:rows[3].path.replaceAll('/','\\')}])[0].name,"King's Row (Winter)");
assert.equal(buildMapLibrary([rows[0],{...rows[0],path:rows[0].path.toUpperCase()}]).length,1);
assert.equal(buildMapLibrary([rows[0],{...rows[0],path:rows[0].path.toUpperCase()}])[0].count,1,'Windows file paths deduplicate case-insensitively.');
const linux=buildMapLibrary([{path:'/tmp/Example.owmap',name:'Example',relativePath:'Example.owmap'},
  {path:'/tmp/example.owmap',name:'example',relativePath:'example.owmap'}]);
assert.equal(linux.length,2,'Case-sensitive paths do not merge on non-Windows volumes.');
assert.ok(linux.every(node=>node.kind==='variant'),'Direct map files do not get a redundant parent.');
const folders=buildMapLibrary([{path:'/library/Colosseo/B34/Day.owmap',relativePath:'Colosseo/B34/Day.owmap'}]);
assert.equal(folders[0].name,'Colosseo');assert.equal(folders[0].children[0].name,'B34');
const explicit=buildMapLibrary([{path:'/unknown/Day.owmap',name:'Day',mapName:'Colosseo',variantGroup:'Build/Mode'}]);
assert.equal(explicit[0].children[0].children[0].name,'Mode');
assert.equal(buildMapLibrary([{path:'/x/Maps/A/B/C.owmap',mapName:'A'}])[0].children[0].name,'B');
assert.deepEqual(buildMapLibrary([null,{}, {path:''}]),[]);
console.log('Map library grouping, natural ordering and search passed.');
