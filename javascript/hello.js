
function main(){

    let name = "Nyan8";
    if(typeof nyanAllParams.name !== "undefined")
    {
        name = nyanAllParams.name;
    }
    return JSON.stringify({
        "success": true,
        "status": 200,
        "data": {
           "message": "hello! " + name
        },
    });
}

main();